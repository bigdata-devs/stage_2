/**
 * Integration benchmark of the Boogle Engine Search Service: GET /search under concurrent load.
 *
 * What it measures
 *   - Query response time in milliseconds (average and p95, plus median, p90, p99 and max) of
 *     every successful search, overall and separately for each concurrency level.
 *   - Throughput (requests per second) and error rate at each concurrency level.
 *
 * Load profile (closed model)
 *   Stepped ramping-vus profile: for every level in VU_LEVELS the VUs ramp up to the level and
 *   hold it, then everything ramps down to 0. Only the "hold" phases count in the per-level
 *   results, so ramps never blur the steady-state numbers. With THINK_TIME=0 every VU always has
 *   exactly one request in flight, so the VU count is the client concurrency.
 *
 * Workload
 *   Each request draws a keyword, optional author/language/year filters and a page
 *   (offset/limit) from weighted lists, including keywords without matches. A seeded generator
 *   per VU makes the request mix identical between runs, so configurations stay comparable.
 *
 * Configuration (environment variables, all optional)
 *   BASE_URL       Search Service base URL                        default http://localhost:7003
 *   VU_LEVELS      comma-separated concurrency levels             default 1,10,50,100
 *   RAMP_DURATION  time to reach each level                       default 10s
 *   HOLD_DURATION  time spent at each level                       default 30s
 *   THINK_TIME     pause between a VU's requests, in seconds      default 0
 *   SEED           seed of the request mix                        default 2026
 *   AVG_SLO_MS     threshold on the average query time           default 200
 *   P95_SLO_MS     threshold on the p95 query time (per level)    default 500
 *   RESULTS_DIR    existing folder for the JSON and CSV reports   default test/load/results
 *
 * Run it from the repository root, because k6 does not create RESULTS_DIR. In Stage 2 the client
 * and the service share one machine: record the host specification with every result, and
 * remember that k6 competes with the service for CPU, especially at high VU counts.
 */
import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Rate, Trend } from 'k6/metrics';

// ---------------------------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------------------------

const BASE_URL = (__ENV.BASE_URL || 'http://localhost:7003').replace(/\/+$/, '');
const VU_LEVELS = parseLevels(__ENV.VU_LEVELS || '1,10,50,100');
const RAMP_DURATION = __ENV.RAMP_DURATION || '10s';
const HOLD_DURATION = __ENV.HOLD_DURATION || '30s';
const THINK_TIME_SECONDS = parseNumber('THINK_TIME', 0);
const SEED = parseNumber('SEED', 2026);
const AVG_SLO_MS = parseNumber('AVG_SLO_MS', 200);
const P95_SLO_MS = parseNumber('P95_SLO_MS', 500);
const RESULTS_DIR = (__ENV.RESULTS_DIR || 'test/load/results').replace(/\/+$/, '');

const REQUEST_NAME = 'GET /search';
const PHASES = buildPhases(VU_LEVELS, RAMP_DURATION, HOLD_DURATION);

// Weighted vocabularies. Matching keywords hit titles of the StubEngine sample catalog, the
// others return 200 with an empty page, so both the "matches" and "no matches" paths are used.
const KEYWORDS = [
  { value: 'adventure', weight: 12 }, { value: 'love', weight: 10 }, { value: 'science', weight: 8 },
  { value: 'island', weight: 6 }, { value: 'pride', weight: 6 }, { value: 'whale', weight: 5 },
  { value: 'sherlock', weight: 5 }, { value: 'war', weight: 5 }, { value: 'history', weight: 4 },
  { value: 'wonderland', weight: 4 }, { value: 'monte', weight: 3 }, { value: 'ocean', weight: 3 },
  { value: 'prometheus', weight: 2 }, { value: 'detective', weight: 2 }, { value: 'quijote', weight: 2 },
  { value: 'verwandlung', weight: 1 }, { value: 'journey', weight: 1 }, { value: 'the', weight: 1 },
];
const AUTHORS = [
  { value: 'Twain', weight: 4 }, { value: 'Austen', weight: 3 }, { value: 'Doyle', weight: 3 },
  { value: 'Shelley', weight: 2 }, { value: 'Melville', weight: 2 }, { value: 'Stevenson', weight: 2 },
  { value: 'Dickens', weight: 2 }, { value: 'Cervantes', weight: 1 }, { value: 'Dumas', weight: 1 },
];
const LANGUAGES = [
  { value: 'en', weight: 14 }, { value: 'es', weight: 2 }, { value: 'fr', weight: 2 }, { value: 'de', weight: 2 },
];
const YEARS = [
  { value: 1605, weight: 1 }, { value: 1719, weight: 1 }, { value: 1813, weight: 2 }, { value: 1818, weight: 1 },
  { value: 1851, weight: 1 }, { value: 1865, weight: 1 }, { value: 1876, weight: 1 }, { value: 1884, weight: 2 },
  { value: 1892, weight: 1 }, { value: 1915, weight: 1 }, { value: 1900, weight: 1 },
];
const PAGE_SIZES = [
  { value: 20, weight: 10 }, { value: 10, weight: 5 }, { value: 50, weight: 3 }, { value: 100, weight: 2 },
];
const PAGE_NUMBERS = [
  { value: 0, weight: 12 }, { value: 1, weight: 4 }, { value: 2, weight: 2 }, { value: 5, weight: 1 }, { value: 10, weight: 1 },
];
const FILTER_PROBABILITY = { author: 0.25, language: 0.35, year: 0.15 };

// ---------------------------------------------------------------------------------------------
// Metrics and options
// ---------------------------------------------------------------------------------------------

/** Response time of every successful query (HTTP 200 with a valid JSON body), in milliseconds. */
const queryResponseTime = new Trend('query_response_time', true);
/** Share of successful queries whose page came back empty. */
const emptyResults = new Rate('empty_results');

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
    query_response_time: [`avg<${AVG_SLO_MS}`, `p(95)<${P95_SLO_MS}`],
  };
  // Thresholds on per-phase sub-metrics also make k6 compute them for the end-of-test summary.
  for (const phase of holdPhases()) {
    thresholds[`query_response_time{phase:${phase.name}}`] = [`p(95)<${P95_SLO_MS}`];
    thresholds[`http_req_failed{phase:${phase.name}}`] = ['rate<0.01'];
    thresholds[`http_reqs{phase:${phase.name}}`] = ['count>0'];
  }
  return thresholds;
}

// ---------------------------------------------------------------------------------------------
// Test lifecycle
// ---------------------------------------------------------------------------------------------

/** Each VU gets its own seeded generator, so the request mix is reproducible run after run. */
const random = seededRandom(SEED * 10007 + __VU);

/** Fails fast with a clear message when the Search Service is not reachable. */
export function setup() {
  const response = http.get(`${BASE_URL}/search?q=adventure`, { tags: { name: 'smoke test' } });
  if (response.status !== 200) {
    exec.test.abort(`Search Service not ready at ${BASE_URL}: HTTP ${response.status} ${response.error || ''}`);
  }
  return { startedAt: new Date().toISOString() };
}

export default function searchBooks() {
  const query = randomQuery();
  const phase = currentPhase().name;
  const response = http.get(`${BASE_URL}/search?${toQueryString(query)}`, { tags: { name: REQUEST_NAME, phase } });
  const body = parseBody(response);
  check(response, {
    'status is 200': (res) => res.status === 200,
    'results is a JSON array': () => Array.isArray(body.results),
    'page respects limit': () => Array.isArray(body.results) && body.results.length <= query.limit,
    'pagination is echoed': () => body.offset === query.offset && body.limit === query.limit,
  });
  recordQuery(response, body, phase);
  if (THINK_TIME_SECONDS > 0) {
    sleep(THINK_TIME_SECONDS);
  }
}

function recordQuery(response, body, phase) {
  if (response.status !== 200 || !Array.isArray(body.results)) {
    return;
  }
  queryResponseTime.add(response.timings.duration, { phase });
  emptyResults.add(body.results.length === 0, { phase });
}

// ---------------------------------------------------------------------------------------------
// Request generation
// ---------------------------------------------------------------------------------------------

/** Draws one search: a keyword, optional filters and a page. */
function randomQuery() {
  const limit = weightedPick(PAGE_SIZES);
  const query = { q: weightedPick(KEYWORDS), offset: weightedPick(PAGE_NUMBERS) * limit, limit };
  if (random() < FILTER_PROBABILITY.author) {
    query.author = weightedPick(AUTHORS);
  }
  if (random() < FILTER_PROBABILITY.language) {
    query.language = weightedPick(LANGUAGES);
  }
  if (random() < FILTER_PROBABILITY.year) {
    query.year = weightedPick(YEARS);
  }
  return query;
}

function weightedPick(entries) {
  const totalWeight = entries.reduce((sum, entry) => sum + entry.weight, 0);
  let ticket = random() * totalWeight;
  for (const entry of entries) {
    ticket -= entry.weight;
    if (ticket < 0) {
      return entry.value;
    }
  }
  return entries[entries.length - 1].value;
}

function toQueryString(query) {
  return Object.keys(query)
    .map((key) => `${encodeURIComponent(key)}=${encodeURIComponent(query[key])}`)
    .join('&');
}

/** Returns the parsed JSON body, or an empty object when the body is not valid JSON. */
function parseBody(response) {
  try {
    return response.json() || {};
  } catch (error) {
    return {};
  }
}

/** Mulberry32: a tiny, fast, seedable pseudo-random generator returning numbers in [0, 1). */
function seededRandom(seed) {
  let state = seed >>> 0;
  return function next() {
    state = (state + 0x6d2b79f5) >>> 0;
    let mixed = Math.imul(state ^ (state >>> 15), 1 | state);
    mixed = (mixed + Math.imul(mixed ^ (mixed >>> 7), 61 | mixed)) ^ mixed;
    return ((mixed ^ (mixed >>> 14)) >>> 0) / 4294967296;
  };
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
 * Prints the per-concurrency-level table and writes the same rows to CSV plus the complete k6
 * summary to JSON, so the report's plots can be produced straight from the files.
 */
export function handleSummary(data) {
  const levels = holdPhases().map((phase) => levelRow(data, phase));
  const stamp = new Date().toISOString().replace(/[:.]/g, '-');
  const basePath = `${RESULTS_DIR}/search_benchmark_${stamp}`;
  return {
    stdout: textReport(data, levels, basePath),
    [`${basePath}.csv`]: csvReport(levels),
    [`${basePath}.json`]: JSON.stringify({ profile: profileDescription(), levels, k6: data }, null, 2),
  };
}

function levelRow(data, phase) {
  const times = metricValues(data, `query_response_time{phase:${phase.name}}`);
  const requests = metricValues(data, `http_reqs{phase:${phase.name}}`).count || 0;
  return {
    vus: phase.target,
    requests,
    throughput_rps: round(requests / phase.seconds),
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
  const overall = metricValues(data, 'query_response_time');
  const lines = [
    '',
    `Boogle Engine search benchmark  ${BASE_URL}/search`,
    `profile: levels ${VU_LEVELS.join(', ')} VUs, ramp ${RAMP_DURATION}, hold ${HOLD_DURATION}, ` +
      `think time ${THINK_TIME_SECONDS}s, seed ${SEED}`,
    '',
    'Steady state per concurrency level (hold phases only)',
    formatRow(['VUs', 'requests', 'req/s', 'avg ms', 'p95 ms', 'p99 ms', 'max ms', 'errors']),
    ...levels.map((level) => formatRow([
      level.vus, level.requests, level.throughput_rps, level.avg_ms, level.p95_ms, level.p99_ms, level.max_ms,
      `${round(level.error_rate * 100, 2)}%`,
    ])),
    '',
    `Query response time, whole run: avg ${round(overall.avg)} ms, p95 ${round(overall['p(95)'])} ms, ` +
      `p99 ${round(overall['p(99)'])} ms over ${overall.count || 0} successful queries`,
    `Requests: ${metricValues(data, 'http_reqs').count || 0}, failed ` +
      `${round((metricValues(data, 'http_req_failed').rate || 0) * 100, 2)}%, empty pages ` +
      `${round((metricValues(data, 'empty_results').rate || 0) * 100, 1)}%`,
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
