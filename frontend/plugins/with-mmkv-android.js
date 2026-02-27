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
    // MMKV doesn't ship armeabi-v7a native libs, so only include it when building
    // for architectures that support it (arm64-v8a, x86_64). This allows TV builds
    // (armeabi-v7a only) to succeed without MMKV.
    const mmkvDependency = `
    def mmkvTargetArchs = (findProperty('reactNativeArchitectures') ?: 'arm64-v8a').split(',')*.trim()
    if (mmkvTargetArchs.contains('arm64-v8a') || mmkvTargetArchs.contains('x86_64')) {
        // Required for @kesha-antonov/react-native-background-downloader release builds.
        implementation("com.tencent:mmkv-shared:${mmkvVersion}")
    }`;
    config.modResults.contents =
      contents.slice(0, insertPosition) + mmkvDependency + contents.slice(insertPosition);

    return config;
  });
};

module.exports = withMmkvAndroid;
