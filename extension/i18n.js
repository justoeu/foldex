export function t(chromeApi, key, substitutions) {
  return chromeApi.i18n.getMessage(key, substitutions);
}
