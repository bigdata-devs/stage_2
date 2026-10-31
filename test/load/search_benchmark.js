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
 *   Each request draws a query, optional author/language/year filters and a page (offset/limit)
 *   from weighted lists. Queries use real vocabulary measured on Project Gutenberg books with the
 *   indexer's own tokenizer, in bands of document frequency, so the benchmark exercises long,
 *   medium and short posting lists. One query in five is a two-word phrase (GET /search ANDs the
 *   words), and a few are words no book contains or a lone stop word. A seeded generator per VU
 *   makes the request mix identical between runs, so configurations stay comparable.
 *
 * Corpus guard
 *   setup() reads GET /index/status and aborts unless at least MIN_BOOKS books are published, so
 *   a benchmark never measures an empty or tiny datamart by mistake. It then sends every
 *   vocabulary word and phrase once and reports the ones without a match; it aborts when most of
 *   the common words match nothing, which means the datamart was not seeded with real books.
 *   Seed one with test/load/seed_corpus.sh. Both results go into the end-of-test report.
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
 *   INDEXING_URL   Indexing Service base URL for the corpus guard default http://localhost:7002
 *   MIN_BOOKS      published books required before the run starts default 300 (0 disables)
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
const INDEXING_URL = (__ENV.INDEXING_URL || 'http://localhost:7002').replace(/\/+$/, '');
const MIN_BOOKS = parseNumber('MIN_BOOKS', 300);

const REQUEST_NAME = 'GET /search';
const PHASES = buildPhases(VU_LEVELS, RAMP_DURATION, HOLD_DURATION);

// Real vocabulary. Every word was measured on 22 Project Gutenberg books (the 18 Gutenberg texts
// of the NLTK corpus plus fixture books 82, 84, 1341 and 1342) with the indexer's tokenizer; the
// comment after each group gives how many of those 22 books contain each word. Weights follow how
// often a reader would type the word, so common words dominate, as in real search traffic.

/** In 19-22 of 22 books: posting lists span almost the whole corpus, the most expensive join. */
const COMMON_WORDS = [
  { value: 'love', weight: 10 }, { value: 'death', weight: 6 }, { value: 'heart', weight: 5 }, // 19 22 20
  { value: 'night', weight: 5 }, { value: 'house', weight: 5 }, { value: 'life', weight: 5 }, // 21 21 22
  { value: 'sea', weight: 5 }, { value: 'father', weight: 4 }, { value: 'mother', weight: 4 }, // 19 20 20
  { value: 'friend', weight: 4 }, { value: 'peace', weight: 3 }, { value: 'water', weight: 3 }, // 21 19 21
  { value: 'world', weight: 3 }, { value: 'time', weight: 3 }, // 22 22
];
/** In 8-18 of 22 books: medium posting lists. */
const MID_WORDS = [
  { value: 'king', weight: 5 }, { value: 'war', weight: 5 }, { value: 'ship', weight: 4 }, // 18 13 15
  { value: 'queen', weight: 3 }, { value: 'garden', weight: 3 }, { value: 'letter', weight: 3 }, // 17 18 17
  { value: 'money', weight: 3 }, { value: 'church', weight: 3 }, { value: 'gold', weight: 3 }, // 17 17 18
  { value: 'horse', weight: 3 }, { value: 'journey', weight: 3 }, { value: 'river', weight: 3 }, // 18 17 14
  { value: 'history', weight: 3 }, { value: 'adventure', weight: 3 }, { value: 'dinner', weight: 2 }, // 16 13 18
  { value: 'storm', weight: 2 }, { value: 'soldier', weight: 2 }, { value: 'winter', weight: 2 }, // 15 16 16
  { value: 'marriage', weight: 2 }, { value: 'fortune', weight: 2 }, { value: 'mountain', weight: 2 }, // 15 15 14
  { value: 'forest', weight: 2 }, { value: 'doctor', weight: 2 }, { value: 'prison', weight: 2 }, // 13 13 13
  { value: 'sword', weight: 2 }, { value: 'captain', weight: 2 }, { value: 'battle', weight: 2 }, // 13 12 11
  { value: 'science', weight: 2 }, { value: 'ocean', weight: 2 }, { value: 'castle', weight: 2 }, // 11 11 9
  { value: 'island', weight: 2 }, { value: 'wedding', weight: 1 }, { value: 'tiger', weight: 1 }, // 8 13 8
];
/** In 1-7 of 22 books: short posting lists. */
const RARE_WORDS = [
  { value: 'whale', weight: 2 }, { value: 'monster', weight: 2 }, { value: 'elephant', weight: 1 }, // 7 7 6
  { value: 'rabbit', weight: 1 }, { value: 'telescope', weight: 1 }, { value: 'volcano', weight: 1 }, // 6 5 5
  { value: 'balloon', weight: 1 }, { value: 'telegraph', weight: 1 }, { value: 'detective', weight: 1 }, // 4 3 3
  { value: 'harpoon', weight: 1 }, { value: 'vampire', weight: 1 }, { value: 'guillotine', weight: 1 }, // 2 2 1
  { value: 'locomotive', weight: 1 }, { value: 'microscope', weight: 1 }, // 1 1
];
/** Characters of books inside Gutenberg IDs 1-300 (Alice #11, Moby Dick #15, Frankenstein #84, Shakespeare #100). */
const NAMED_WORDS = [
  { value: 'alice', weight: 2 }, { value: 'hamlet', weight: 2 }, // 3 8
  { value: 'ahab', weight: 1 }, { value: 'frankenstein', weight: 1 }, // 2 1
];
/** Anachronisms no 19th-century book contains: the "no matches" path, answered 200 with results []. */
const NO_MATCH_WORDS = [
  { value: 'quidditch', weight: 1 }, { value: 'smartphone', weight: 1 }, { value: 'spaceship', weight: 1 }, // 0 0 0
];
/** A lone stop word is never indexed: the Search Service answers it without querying MongoDB. */
const STOP_WORD_ONLY = [{ value: 'the', weight: 1 }];

const KEYWORDS = [...COMMON_WORDS, ...MID_WORDS, ...RARE_WORDS, ...NAMED_WORDS, ...NO_MATCH_WORDS, ...STOP_WORD_ONLY];

/** Two-word queries, which GET /search answers with the books holding both words (AND). */
const PHRASES = [
  { value: 'old man', weight: 3 }, { value: 'father mother', weight: 2 }, { value: 'dark night', weight: 2 }, // 22 19 18
  { value: 'young lady', weight: 2 }, { value: 'king queen', weight: 2 }, { value: 'sea ship', weight: 2 }, // 18 15 15
  { value: 'love letter', weight: 2 }, { value: 'war peace', weight: 2 }, { value: 'poor child', weight: 1 }, // 14 13 17
  { value: 'gold money', weight: 1 }, { value: 'storm sea', weight: 1 }, { value: 'horse soldier', weight: 1 }, // 15 14 14
  { value: 'river mountain', weight: 1 }, { value: 'captain ship', weight: 1 }, { value: 'white whale', weight: 1 }, // 12 11 7
  { value: 'mad hatter', weight: 1 }, // 3
];
const PHRASE_PROBABILITY = 0.2;

/** Vocabulary bands, in the order the coverage report lists them. */
const VOCABULARY_BANDS = [
  { name: 'common', entries: COMMON_WORDS }, { name: 'mid', entries: MID_WORDS }, { name: 'rare', entries: RARE_WORDS },
  { name: 'named', entries: NAMED_WORDS }, { name: 'phrase', entries: PHRASES },
];

// Filter values of real Gutenberg authors and languages. Most Gutenberg headers carry no original
// publication year (publication_year stays 0), so year filters mostly exercise the empty path.
const AUTHORS = [
  { value: 'Twain', weight: 4 }, { value: 'Austen', weight: 3 }, { value: 'Doyle', weight: 3 },
  { value: 'Dickens', weight: 3 }, { value: 'Shakespeare', weight: 2 }, { value: 'Wells', weight: 2 },
  { value: 'Verne', weight: 2 }, { value: 'Stevenson', weight: 2 }, { value: 'Shelley', weight: 1 },
  { value: 'Melville', weight: 1 }, { value: 'Carroll', weight: 1 }, { value: 'Wilde', weight: 1 },
  { value: 'Kipling', weight: 1 },
];
const LANGUAGES = [
  { value: 'en', weight: 16 }, { value: 'fr', weight: 2 }, { value: 'de', weight: 1 }, { value: 'es', weight: 1 },
];
const YEARS = [
  { value: 1813, weight: 1 }, { value: 1818, weight: 1 }, { value: 1851, weight: 1 }, { value: 1865, weight: 1 },
  { value: 1876, weight: 1 }, { value: 1883, weight: 1 }, { value: 1884, weight: 1 }, { value: 1895, weight: 1 },
  { value: 1897, weight: 1 }, { value: 1898, weight: 1 },
];
const PAGE_SIZES = [
  { value: 20, weight: 10 }, { value: 10, weight: 5 }, { value: 50, weight: 3 }, { value: 100, weight: 2 },
];
const PAGE_NUMBERS = [
  { value: 0, weight: 12 }, { value: 1, weight: 4 }, { value: 2, weight: 2 }, { value: 5, weight: 1 }, { value: 10, weight: 1 },
];
const FILTER_PROBABILITY = { author: 0.25, language: 0.35, year: 0.1 };

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

/**
 * Fails fast when the Search Service is not reachable or the datamart is not the intended corpus,
 * then measures the vocabulary coverage reported at the end of the test.
 */
export function setup() {
  const response = http.get(`${BASE_URL}/search?q=adventure`, { tags: { name: 'smoke test' } });
  if (response.status !== 200) {
    exec.test.abort(`Search Service not ready at ${BASE_URL}: HTTP ${response.status} ${response.error || ''}`);
  }
  const corpusBooks = publishedBooks();
  if (MIN_BOOKS > 0 && corpusBooks !== null && corpusBooks < MIN_BOOKS) {
    exec.test.abort(`the datamart holds ${corpusBooks} published books, fewer than MIN_BOOKS=${MIN_BOOKS}; ` +
      'seed it with test/load/seed_corpus.sh or set MIN_BOOKS=0');
  }
  const coverage = vocabularyCoverage();
  const commonMisses = coverage.zeroHit.filter((entry) => entry.band === 'common').length;
  if (commonMisses > COMMON_WORDS.length / 2) {
    exec.test.abort(`${commonMisses} of ${COMMON_WORDS.length} common words match no book: the datamart is not ` +
      'seeded with real books; run test/load/seed_corpus.sh first');
  }
  return { startedAt: new Date().toISOString(), corpusBooks, coverage };
}

/** Number of published books from GET /index/status, or null when the Indexing Service is not reachable. */
function publishedBooks() {
  const response = http.get(`${INDEXING_URL}/index/status`, { tags: { name: 'corpus check' } });
  const body = parseBody(response);
  if (response.status !== 200 || typeof body.books_indexed !== 'number') {
    console.warn(`corpus size unknown: GET ${INDEXING_URL}/index/status answered HTTP ${response.status}`);
    return null;
  }
  return body.books_indexed;
}

/** Sends every vocabulary word and phrase once and lists the ones that match no book. */
function vocabularyCoverage() {
  const zeroHit = [];
  let checked = 0;
  for (const band of VOCABULARY_BANDS) {
    for (const entry of band.entries) {
      const response = http.get(`${BASE_URL}/search?${toQueryString({ q: entry.value, limit: 1 })}`,
        { tags: { name: 'vocabulary check' } });
      checked += 1;
      if (parseBody(response).total === 0) {
        zeroHit.push({ band: band.name, value: entry.value });
      }
    }
  }
  if (zeroHit.length > 0) {
    console.warn(`vocabulary without matches in this datamart: ${zeroHit.map((entry) => entry.value).join(', ')}`);
  }
  return { checked, zeroHit };
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

/** Draws one search: a keyword or a two-word phrase, optional filters and a page. */
function randomQuery() {
  const limit = weightedPick(PAGE_SIZES);
  const q = random() < PHRASE_PROBABILITY ? weightedPick(PHRASES) : weightedPick(KEYWORDS);
  const query = { q, offset: weightedPick(PAGE_NUMBERS) * limit, limit };
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
    [`${basePath}.json`]: JSON.stringify({ profile: profileDescription(data.setup_data), levels, k6: data }, null, 2),
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
    corpusLine(data.setup_data),
    vocabularyLine(data.setup_data),
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

/** Describes the datamart the run measured, from what setup() found. */
function corpusLine(setupData) {
  const books = setupData && setupData.corpusBooks;
  return `corpus: ${books === null || books === undefined ? 'unknown number of' : books} published books ` +
    `(GET ${INDEXING_URL}/index/status, MIN_BOOKS ${MIN_BOOKS})`;
}

function vocabularyLine(setupData) {
  const coverage = setupData && setupData.coverage;
  if (!coverage) {
    return 'vocabulary: not checked';
  }
  const misses = coverage.zeroHit.map((entry) => `${entry.value} (${entry.band})`);
  return `vocabulary: ${coverage.checked - misses.length} of ${coverage.checked} words and phrases match at least one book` +
    (misses.length > 0 ? `; no match: ${misses.join(', ')}` : '');
}

function profileDescription(setupData) {
  return {
    corpus_books: setupData ? setupData.corpusBooks : null,
    vocabulary_checked: setupData && setupData.coverage ? setupData.coverage.checked : null,
    vocabulary_without_matches: setupData && setupData.coverage ? setupData.coverage.zeroHit : null,
    phrase_probability: PHRASE_PROBABILITY,
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
