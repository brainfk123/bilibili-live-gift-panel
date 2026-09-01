import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';

const viewports = [
  ['desktop-1440x900', 1440, 900],
  ['narrow-1024x768', 1024, 768],
  ['mobile-390x844', 390, 844],
];
const features = [
  ['overview', ['empty', 'populated', 'loading', 'error', 'readonly', 'disabled', 'focus-visible', 'overlay-open'], ['navigate', 'refresh', 'open-settings']],
  ['attributes', ['empty', 'populated', 'loading', 'error', 'readonly', 'disabled', 'focus-visible', 'overlay-open', 'editing', 'validation-error'], ['create', 'edit', 'save', 'cancel', 'delete-confirm']],
  ['activities', ['empty', 'populated', 'loading', 'error', 'readonly', 'disabled', 'focus-visible', 'overlay-open', 'active', 'locked', 'settled'], ['create', 'start', 'lock', 'settle', 'cancel']],
  ['gift-targets', ['empty', 'populated', 'loading', 'error', 'readonly', 'disabled', 'focus-visible', 'overlay-open', 'editing'], ['create', 'edit', 'save', 'cancel', 'delete-confirm']],
  ['obs', ['empty', 'populated', 'loading', 'error', 'readonly', 'disabled', 'focus-visible', 'overlay-open', 'preview'], ['create', 'preview', 'copy', 'reset-link', 'delete-confirm']],
  ['analytics', ['empty', 'populated', 'loading', 'error', 'readonly', 'disabled', 'focus-visible', 'overlay-open', 'filtered'], ['filter', 'paginate', 'open-viewer', 'clear-history']],
];
const compare = ['structure', 'hierarchy', 'spacing', 'controls', 'states', 'responsive', 'interactions'];

function fail(message) { throw new Error(`Invalid UI parity requirements: ${message}`); }
function object(value, context) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail(`${context} must be an object`);
  return value;
}
function exactKeys(value, keys, context) {
  const actual = Object.keys(object(value, context)).sort();
  const expected = [...keys].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) fail(`${context} has unknown or missing keys`);
}
function sameArray(actual, expected, context) {
  if (!Array.isArray(actual) || actual.length !== expected.length || actual.some((item, index) => item !== expected[index])) fail(`${context} is not allowlisted`);
}
function cleanString(value, context) {
  if (typeof value !== 'string' || !value) fail(`${context} must be a non-empty string`);
  if (/^(?:[A-Za-z]:[\\/]|\/|https?:\/\/)|[?&]token=/i.test(value)) fail(`${context} contains an unsafe path or URL`);
  return value;
}

export function validateUIParityRequirements(value) {
  exactKeys(value, ['schema', 'reference', 'viewports', 'features'], 'root');
  if (value.schema !== 1) fail('schema must be 1');
  exactKeys(value.reference, ['product', 'minimumVersion'], 'reference');
  if (cleanString(value.reference.product, 'reference.product') !== 'gift-panel-exe') fail('reference.product is not allowlisted');
  if (cleanString(value.reference.minimumVersion, 'reference.minimumVersion') !== '0.4.10') fail('reference.minimumVersion is not allowlisted');
  if (!Array.isArray(value.viewports) || value.viewports.length !== viewports.length) fail('viewports is incomplete');
  if (!Array.isArray(value.features) || value.features.length !== features.length) fail('features is incomplete');

  const outputViewports = value.viewports.map((item, index) => {
    const [id, width, height] = viewports[index];
    exactKeys(item, ['id', 'width', 'height', 'deviceScaleFactor'], `viewports[${index}]`);
    if (cleanString(item.id, `viewports[${index}].id`) !== id || item.width !== width || item.height !== height || item.deviceScaleFactor !== 1) fail(`viewports[${index}] is not allowlisted`);
    return { id, width, height, deviceScaleFactor: 1 };
  });
  if (new Set(outputViewports.map((item) => item.id)).size !== viewports.length) fail('duplicate viewport IDs');

  const outputFeatures = value.features.map((item, index) => {
    const [id, states, interactions] = features[index];
    exactKeys(item, ['id', 'states', 'interactions'], `features[${index}]`);
    if (cleanString(item.id, `features[${index}].id`) !== id) fail(`features[${index}].id is not allowlisted`);
    sameArray(item.states, states, `features[${index}].states`);
    sameArray(item.interactions, interactions, `features[${index}].interactions`);
    return { id, states: [...states], interactions: [...interactions], compare: [...compare] };
  });
  if (new Set(outputFeatures.map((item) => item.id)).size !== features.length) fail('duplicate feature IDs');
  return { schema: 1, reference: { product: 'gift-panel-exe', minimumVersion: '0.4.10' }, viewports: outputViewports, features: outputFeatures };
}

const isMain = process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url;
if (isMain) {
  const input = process.argv[2];
  if (!input) fail('a requirements file is required');
  console.log(JSON.stringify(validateUIParityRequirements(JSON.parse(readFileSync(input, 'utf8')))));
}
