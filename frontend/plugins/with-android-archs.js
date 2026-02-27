const { withGradleProperties } = require('@expo/config-plugins');

/**
 * Forces reactNativeArchitectures in gradle.properties during prebuild.
 * expo-build-properties doesn't reliably write this value, so we do it explicitly.
 *
 * Non-TV builds: arm64-v8a, x86_64 (MMKV doesn't support armeabi-v7a)
 * TV builds: armeabi-v7a, arm64-v8a, x86_64
 */
const withAndroidArchs = (config) => {
  return withGradleProperties(config, (config) => {
    const isTV = process.env.EXPO_TV === '1';
    const archs = isTV
      ? 'armeabi-v7a,arm64-v8a,x86_64'
      : 'arm64-v8a,x86_64';

    const props = config.modResults;
    const existing = props.find(
      (p) => p.type === 'property' && p.key === 'reactNativeArchitectures'
    );

    if (existing) {
      existing.value = archs;
    } else {
      props.push({
        type: 'property',
        key: 'reactNativeArchitectures',
        value: archs,
      });
    }

    return config;
  });
};

module.exports = withAndroidArchs;
