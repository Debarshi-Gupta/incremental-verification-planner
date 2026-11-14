'use strict';

// IVP JUnit reporter for Jest.
//
// The IVP jest adapter materializes this module into the workspace and hands
// it to jest through --reporters, so every selective jest run also produces a
// JUnit XML report at the path the adapter declares in
// ExecutionCommand.ReportPaths. The report path is baked into the module at
// materialization time, because jest does not accept reporter options on the
// command line.
//
// The module is also runnable on its own:
//
//     node ivp-junit-reporter.js <aggregated-result.json> [rootDir]
//
// which is how the adapter's tests exercise the XML writer without a jest
// installation.

const fs = require('fs');
const path = require('path');

// Replaced by the adapter with the absolute path of the JUnit report.
const OUTPUT_PATH = '__IVP_JUNIT_OUTPUT_PATH__';

function escapeXML(value) {
  return String(value === undefined || value === null ? '' : value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&apos;')
    .replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F]/g, '');
}

function seconds(milliseconds) {
  return (Number(milliseconds) || 0) / 1000;
}

// suitePath renders the test file of a suite relative to the jest project root
// so the report does not embed machine specific absolute paths.
function suitePath(rootDir, filePath) {
  if (!filePath) {
    return '';
  }
  if (!rootDir) {
    return filePath;
  }
  const relative = path.relative(rootDir, filePath);
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative)) {
    return filePath;
  }
  return relative.split(path.sep).join('/');
}

function firstLine(messages) {
  const text = (messages || []).join('\n');
  const index = text.indexOf('\n');
  return index === -1 ? text : text.slice(0, index);
}

function isSkipped(status) {
  return (
    status === 'pending' ||
    status === 'todo' ||
    status === 'skipped' ||
    status === 'disabled'
  );
}

function buildTestCase(rootDir, suite, assertion) {
  const status = String(assertion.status || '').toLowerCase();
  const duration = Number(assertion.duration) || 0;
  const className = escapeXML(suitePath(rootDir, suite.testFilePath || suite.name));
  const name = escapeXML(assertion.fullName || assertion.title || '');
  const time = seconds(duration).toFixed(3);
  let body = '';
  if (status === 'failed') {
    const messages = Array.isArray(assertion.failureMessages)
      ? assertion.failureMessages
      : [];
    body =
      '<failure message="' +
      escapeXML(firstLine(messages)) +
      '">' +
      escapeXML(messages.join('\n')) +
      '</failure>';
  } else if (isSkipped(status)) {
    body = '<skipped/>';
  }
  return {
    status: status,
    duration: duration,
    xml:
      '<testcase classname="' +
      className +
      '" name="' +
      name +
      '" time="' +
      time +
      '">' +
      body +
      '</testcase>',
  };
}

function buildSuite(rootDir, suite) {
  const assertions = Array.isArray(suite.assertionResults)
    ? suite.assertionResults
    : [];
  const className = escapeXML(suitePath(rootDir, suite.testFilePath || suite.name));
  const cases = [];
  let failures = 0;
  let errors = 0;
  let skipped = 0;
  let time = 0;
  for (const assertion of assertions) {
    const built = buildTestCase(rootDir, suite, assertion);
    cases.push(built.xml);
    time += built.duration;
    if (built.status === 'failed') {
      failures++;
    } else if (isSkipped(built.status)) {
      skipped++;
    }
  }
  if (cases.length === 0 && String(suite.status || '').toLowerCase() === 'failed') {
    // The suite failed before any assertion ran. Report it as an error case
    // so a broken suite is never silently counted as zero tests.
    errors++;
    cases.push(
      '<testcase classname="' +
        className +
        '" name="' +
        className +
        '" time="0.000"><error message="' +
        escapeXML(String(suite.message || 'test suite failed to run')) +
        '"></error></testcase>',
    );
  }
  return {
    tests: cases.length,
    failures: failures,
    errors: errors,
    skipped: skipped,
    time: seconds(time),
    xml:
      '    <testsuite name="' +
      className +
      '" tests="' +
      cases.length +
      '" failures="' +
      failures +
      '" errors="' +
      errors +
      '" skipped="' +
      skipped +
      '" time="' +
      seconds(time).toFixed(3) +
      '">\n      ' +
      cases.join('\n      ') +
      '\n    </testsuite>',
  };
}

function buildXML(rootDir, results) {
  const suites =
    results && Array.isArray(results.testResults) ? results.testResults : [];
  let tests = 0;
  let failures = 0;
  let errors = 0;
  let skipped = 0;
  let time = 0;
  const body = suites.map(suite => {
    const built = buildSuite(rootDir, suite);
    tests += built.tests;
    failures += built.failures;
    errors += built.errors;
    skipped += built.skipped;
    time += built.time;
    return built.xml;
  });
  return (
    '<?xml version="1.0" encoding="utf-8"?>\n' +
    '<testsuites name="jest tests" tests="' +
    tests +
    '" failures="' +
    failures +
    '" errors="' +
    errors +
    '" skipped="' +
    skipped +
    '" time="' +
    time.toFixed(3) +
    '">\n' +
    body.join('\n') +
    '\n</testsuites>\n'
  );
}

function writeReport(rootDir, results) {
  fs.mkdirSync(path.dirname(OUTPUT_PATH), {recursive: true});
  fs.writeFileSync(OUTPUT_PATH, buildXML(rootDir, results));
  return OUTPUT_PATH;
}

function IvpJunitReporter(globalConfig) {
  this._globalConfig = globalConfig || {};
}

IvpJunitReporter.prototype.onTestResult = function () {};

IvpJunitReporter.prototype.onRunComplete = function (contexts, results) {
  writeReport(this._globalConfig.rootDir, results || {});
};

module.exports = IvpJunitReporter;

if (require.main === module) {
  const input = process.argv[2];
  const rootDir = process.argv[3] || '';
  if (!input) {
    process.stderr.write('usage: node ivp-junit-reporter.js <result.json> [rootDir]\n');
    process.exit(2);
  }
  writeReport(rootDir, JSON.parse(fs.readFileSync(input, 'utf8')));
}
