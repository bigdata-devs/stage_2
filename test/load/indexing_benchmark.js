/**
 * Integration benchmark of the Boogle Engine Indexing Service: POST /index/update under
 * concurrent load — how many books per second the service indexes into the MongoDB datamart.
 *
 * What it measures
 *   - Indexing response time in milliseconds (average and p95, plus median, p90, p99 and max)
 *     of every successful update, overall and separately for each concurrency level.
 *   - Throughput in books indexed per second (one successful update indexes one book) and the
 *     error rate at each concurrency level.
 *
 * Load profile (closed model)
 *   Stepped ramping-vus profile: for every level in VU_LEVELS the VUs ramp up to the level and
 *   hold it, then everything ramps down to 0. Only the "hold" phases count in the per-level
 *   results, so ramps never blur the steady-state numbers. With THINK_TIME=0 every VU always has
 *   exactly one request in flight, so the VU count is the client concurrency.
 *
 * Workload
 *   Each request re-indexes one book of BOOK_IDS, drawn round-robin with a seeded offset per VU
 *   so concurrent VUs walk the corpus in different orders. Every book must already be in the
 *   datalake (see "Reproducing" below); an ID outside the datamart answers 404 and counts as an
 *   error. Re-indexing is idempotent, so the corpus may be smaller than the number of requests:
 *   the benchmark measures steady-state indexing throughput, not first-time ingestions.
 *
 * Configuration (environment variables, all optional)
 *   BASE_URL       Indexing Service base URL                       default http://localhost:7002
 *   BOOK_IDS       comma-separated corpus of datalake book IDs     default 11,26,43,... (see below)
 *   VU_LEVELS      comma-separated concurrency levels              default 1,5,10
 *   RAMP_DURATION  time to reach each level                        default 10s
 *   HOLD_DURATION  time spent at each level                        default 30s
 *   THINK_TIME     pause between a VU's requests, in seconds       default 0
 *   SEED           seed of the corpus walk                         default 2026
 *   AVG_SLO_MS     threshold on the average index time             default 1500
 *   P95_SLO_MS     threshold on the p95 index time (per level)     default 3000
 *   RESULTS_DIR    existing folder for the JSON and CSV reports    default test/load/results
 *
 * Run it from the repository root, because k6 does not create RESULTS_DIR. In Stage 2 the client
 * and the service share one machine: record the host specification with every result, sample the
 * service's CPU and memory while it runs (test/load/sample_resources.ps1), and remember that k6
 * competes with the service for CPU, especially at high VU counts. Each update writes the whole
 * book's postings to MongoDB, so this benchmark loads the datamart far harder than GET /search.
 */
import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Trend } from 'k6/metrics';

// ---------------------------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------------------------

const BASE_URL = (__ENV.BASE_URL || 'http://localhost:7002').replace(/\/+$/, '');
const BOOK_IDS = parseBookIDs(__ENV.BOOK_IDS || '11,26,43,46,74,76,84,98,201,521,844,1080,1260,1342,1355,1400,1661,17489,1952,2701');
const VU_LEVELS = parseLevels(__ENV.VU_LEVELS || '1,5,10');
const RAMP_DURATION = __ENV.RAMP_DURATION || '10s';
const HOLD_DURATION = __ENV.HOLD_DURATION || '30s';
const THINK_TIME_SECONDS = parseNumber('THINK_TIME', 0);
const SEED = parseNumber('SEED', 2026);
const AVG_SLO_MS = parseNumber('AVG_SLO_MS', 1500);
const P95_SLO_MS = parseNumber('P95_SLO_MS', 3000);
const RESULTS_DIR = (__ENV.RESULTS_DIR || 'test/load/results').replace(/\/+$/, '');

const REQUEST_NAME = 'POST /index/update/{book_id}';
const PHASES = buildPhases(VU_LEVELS, RAMP_DURATION, HOLD_DURATION);

// ---------------------------------------------------------------------------------------------
// Metrics and options
// ---------------------------------------------------------------------------------------------

/** Response time of every successful update (HTTP 200), in milliseconds. */
const indexResponseTime = new Trend('index_response_time', true);

export const options = {
  stages: PHASES.map((phase) => ({ duration: phase.duration, target: phase.target })),
  thresholds: buildThresholds(),
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max', 'count'],
  // Every URL is different, so the url tag is dropped to keep the number of time series small.
  systemTags: ['status', 'method', 'name', 'group', 'check', 'error', 'error_code', 'scenario', 'expected_response'],
  discardResponseBodies: false,
};

function buildThresholds() {
  const thresholds = {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
    index_response_time: [`avg<${AVG_SLO_MS}`, `p(95)<${P95_SLO_MS}`],
  };
  // Thresholds on per-phase sub-metrics also make k6 compute them for the end-of-test summary.
  for (const phase of holdPhases()) {
    thresholds[`index_response_time{phase:${phase.name}}`] = [`p(95)<${P95_SLO_MS}`];
    thresholds[`http_req_failed{phase:${phase.name}}`] = ['rate<0.01'];
    thresholds[`http_reqs{phase:${phase.name}}`] = ['count>0'];
  }
  return thresholds;
}

// ---------------------------------------------------------------------------------------------
// Test lifecycle
// ---------------------------------------------------------------------------------------------

/** Fails fast with a clear message when the Indexing Service or the corpus is not usable. */
export function setup() {
  const smokeID = BOOK_IDS[0];
  const response = http.post(`${BASE_URL}/index/update/${smokeID}`, null, { tags: { name: 'smoke test' } });
  if (response.status !== 200) {
    exec.test.abort(
      `Indexing Service not ready at ${BASE_URL} (book ${smokeID}): HTTP ${response.status} ${response.error || ''} — ` +
      'is MongoDB running and every BOOK_ID present in the datalake?',
    );
  }
  return { startedAt: new Date().toISOString(), corpus: BOOK_IDS.length };
}

export default function indexBook() {
  const bookID = BOOK_IDS[walkIndex()];
  const phase = currentPhase().name;
  const response = http.post(`${BASE_URL}/index/update/${bookID}`, null, { tags: { name: REQUEST_NAME, phase } });
  const body = parseBody(response);
  check(response, {
    'status is 200': (res) => res.status === 200,
    'the book is reported as updated': () => body.book_id === bookID && body.index === 'updated',
  });
  recordUpdate(response, bookID, phase);
  if (THINK_TIME_SECONDS > 0) {
    sleep(THINK_TIME_SECONDS);
  }
}

function recordUpdate(response, bookID, phase) {
  if (response.status !== 200) {
    return;
  }
  indexResponseTime.add(response.timings.duration, { phase });
}

/**
 * Walks BOOK_IDS round-robin. Each VU starts at its own seeded offset so concurrent clients do
 * not stampede the same book, and the walk is stateless between iterations.
 */
function walkIndex() {
  const offset = (SEED * 10007 + __VU * 31) % BOOK_IDS.length;
  const step = Math.floor(exec.scenario.iterationInTest + offset);
  return step % BOOK_IDS.length;
}

/** Returns the parsed JSON body, or an empty object when the body is not valid JSON. */
function parseBody(response) {
  try {
    return response.json() || {};
  } catch (error) {
    return {};
  }
}

// ---------------------------------------------------------------------------------------------
// Load phases
// ---------------------------------------------------------------------------------------------

/** Expands the levels into ramp/hold stages, each with its time window inside the scenario. */
function buildPhases(levels, rampDuration, holdDuration) {
  const stages = [];
  for (const level of levels) {
    stages.push({ name: `ramp-to-${level}vus`, kind: 'ramp', target: level, duration: rampDuration });
    stages.push({ name: `hold-${level}vus`, kind: 'hold', target: level, duration: holdDuration });
  }
  stages.push({ name: 'ramp-down', kind: 'ramp', target: 0, duration: rampDuration });
  let startSeconds = 0;
  return stages.map((stage) => {
    const seconds = durationToSeconds(stage.duration);
    const phase = { ...stage, seconds, startSeconds, endSeconds: startSeconds + seconds };
    startSeconds += seconds;
    return phase;
  });
}

function holdPhases() {
  return PHASES.filter((phase) => phase.kind === 'hold');
}

/** Finds the phase the scenario is in right now, from the time elapsed since it started. */
function currentPhase() {
  const elapsedSeconds = (Date.now() - exec.scenario.startTime) / 1000;
  return PHASES.find((phase) => elapsedSeconds < phase.endSeconds) || PHASES[PHASES.length - 1];
}

/** Converts k6 durations such as "45s", "2m" or "1m30s" into seconds. */
function durationToSeconds(duration) {
  const parts = String(duration).match(/(\d+(?:\.\d+)?)(ms|h|m|s)/g);
  if (!parts || parts.join('') !== String(duration)) {
    throw new Error(`Unsupported duration "${duration}": use values such as 30s, 2m or 1m30s`);
  }
  const unitSeconds = { ms: 0.001, s: 1, m: 60, h: 3600 };
  return parts.reduce((total, part) => {
    const [, amount, unit] = part.match(/(\d+(?:\.\d+)?)(ms|h|m|s)/);
    return total + Number(amount) * unitSeconds[unit];
  }, 0);
}

function parseLevels(rawLevels) {
  const levels = rawLevels.split(',').map((level) => Number(level.trim()));
  if (levels.some((level) => !Number.isInteger(level) || level < 1)) {
    throw new Error(`VU_LEVELS must be positive integers separated by commas, got "${rawLevels}"`);
  }
  if (new Set(levels).size !== levels.length) {
    throw new Error(`VU_LEVELS must not repeat a level, got "${rawLevels}"`);
  }
  return levels;
}

function parseBookIDs(rawIDs) {
  const ids = rawIDs.split(',').map((id) => Number(id.trim()));
  if (ids.some((id) => !Number.isInteger(id) || id < 1)) {
    throw new Error(`BOOK_IDS must be positive integers separated by commas, got "${rawIDs}"`);
  }
  if (new Set(ids).size !== ids.length) {
    throw new Error(`BOOK_IDS must not repeat an ID, got "${rawIDs}"`);
  }
  return ids;
}

function parseNumber(name, fallback) {
  if (__ENV[name] === undefined || __ENV[name] === '') {
    return fallback;
  }
  const value = Number(__ENV[name]);
  if (!Number.isFinite(value) || value < 0) {
    throw new Error(`${name} must be a non-negative number, got "${__ENV[name]}"`);
  }
  return value;
}

// ---------------------------------------------------------------------------------------------
// End-of-test report
// ---------------------------------------------------------------------------------------------

/**
 * Prints the per-concurrency-level table (with the books/s column this benchmark is about) and
 * writes the same rows to CSV plus the complete k6 summary to JSON, so the report's plots can be
 * produced straight from the files.
 */
export function handleSummary(data) {
  const levels = holdPhases().map((phase) => levelRow(data, phase));
  const stamp = new Date().toISOString().replace(/[:.]/g, '-');
  const basePath = `${RESULTS_DIR}/indexing_benchmark_${stamp}`;
  return {
    stdout: textReport(data, levels, basePath),
    [`${basePath}.csv`]: csvReport(levels),
    [`${basePath}.json`]: JSON.stringify({ profile: profileDescription(), levels, k6: data }, null, 2),
  };
}

function levelRow(data, phase) {
  const times = metricValues(data, `index_response_time{phase:${phase.name}}`);
  const requests = metricValues(data, `http_reqs{phase:${phase.name}}`).count || 0;
  return {
    vus: phase.target,
    books_indexed: requests,
    books_per_second: round(requests / phase.seconds),
    avg_ms: round(times.avg),
    med_ms: round(times.med),
    p90_ms: round(times['p(90)']),
    p95_ms: round(times['p(95)']),
    p99_ms: round(times['p(99)']),
    max_ms: round(times.max),
    error_rate: round(metricValues(data, `http_req_failed{phase:${phase.name}}`).rate, 4),
  };
}

function metricValues(data, name) {
  return (data.metrics[name] && data.metrics[name].values) || {};
}

function textReport(data, levels, basePath) {
  const overall = metricValues(data, 'index_response_time');
  const lines = [
    '',
    `Boogle Engine indexing benchmark  ${BASE_URL}/index/update`,
    `profile: levels ${VU_LEVELS.join(', ')} VUs, ramp ${RAMP_DURATION}, hold ${HOLD_DURATION}, ` +
      `think time ${THINK_TIME_SECONDS}s, seed ${SEED}, corpus ${BOOK_IDS.length} books [${BOOK_IDS.join(', ')}]`,
    '',
    'Steady state per concurrency level (hold phases only)',
    formatRow(['VUs', 'books', 'books/s', 'avg ms', 'p95 ms', 'p99 ms', 'max ms', 'errors']),
    ...levels.map((level) => formatRow([
      level.vus, level.books_indexed, level.books_per_second, level.avg_ms, level.p95_ms, level.p99_ms, level.max_ms,
      `${round(level.error_rate * 100, 2)}%`,
    ])),
    '',
    `Index response time, whole run: avg ${round(overall.avg)} ms, p95 ${round(overall['p(95)'])} ms, ` +
      `p99 ${round(overall['p(99)'])} ms over ${overall.count || 0} successful updates`,
    `Updates: ${metricValues(data, 'http_reqs').count || 0}, failed ` +
      `${round((metricValues(data, 'http_req_failed').rate || 0) * 100, 2)}%`,
    '',
    'Checks',
    ...checkLines(data),
    '',
    'Thresholds',
    ...thresholdLines(data),
    '',
    `Saved ${basePath}.csv and ${basePath}.json`,
    '',
  ];
  return lines.join('\n');
}

function checkLines(data) {
  return data.root_group.checks.map((item) => {
    const total = item.passes + item.fails;
    const passRate = total === 0 ? 0 : (item.passes / total) * 100;
    return `  ${item.fails === 0 ? 'ok  ' : 'FAIL'} ${item.name}: ${round(passRate, 2)}% of ${total}`;
  });
}

function thresholdLines(data) {
  const lines = [];
  for (const [metricName, metric] of Object.entries(data.metrics)) {
    for (const [expression, result] of Object.entries(metric.thresholds || {})) {
      lines.push(`  ${result.ok ? 'ok  ' : 'FAIL'} ${metricName}: ${expression}`);
    }
  }
  return lines.sort();
}

function csvReport(levels) {
  const columns = Object.keys(levels[0] || { vus: 0 });
  const rows = levels.map((level) => columns.map((column) => level[column]).join(','));
  return [columns.join(','), ...rows].join('\n') + '\n';
}

function profileDescription() {
  return {
    base_url: BASE_URL,
    book_ids: BOOK_IDS,
    vu_levels: VU_LEVELS,
    ramp_duration: RAMP_DURATION,
    hold_duration: HOLD_DURATION,
    think_time_seconds: THINK_TIME_SECONDS,
    seed: SEED,
    avg_slo_ms: AVG_SLO_MS,
    p95_slo_ms: P95_SLO_MS,
  };
}

function formatRow(cells) {
  return cells.map((cell, position) => String(cell).padStart(position === 0 ? 5 : 10)).join('');
}

function round(value, decimals = 2) {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return 0;
  }
  const factor = 10 ** decimals;
  return Math.round(value * factor) / factor;
}
