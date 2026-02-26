const { withAppBuildGradle } = require('@expo/config-plugins');

/**
 * Ensures MMKV is present in app dependencies for Android builds.
 * @kesha-antonov/react-native-background-downloader declares MMKV as compileOnly,
 * so the app must provide it to avoid R8 missing-class failures in release builds.
 */
const withMmkvAndroid = (config, options = {}) => {
  const mmkvVersion = options.mmkvVersion || '2.2.4';

  return withAppBuildGradle(config, (config) => {
    const contents = config.modResults.contents;

    if (contents.includes('com.tencent:mmkv') || contents.includes('io.github.zhongwuzw:mmkv')) {
      return config;
    }

    const dependenciesRegex = /dependencies\s*\{/;
    const match = contents.match(dependenciesRegex);
    if (!match) {
      return config;
    }

    const insertPosition = contents.indexOf(match[0]) + match[0].length;
    const mmkvDependency = `\n    // Required for @kesha-antonov/react-native-background-downloader release builds.\n    implementation("com.tencent:mmkv-shared:${mmkvVersion}")`;
    config.modResults.contents =
      contents.slice(0, insertPosition) + mmkvDependency + contents.slice(insertPosition);

    return config;
  });
};

module.exports = withMmkvAndroid;
