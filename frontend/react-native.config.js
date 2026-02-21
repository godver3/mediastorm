const isTV = process.env.EXPO_TV === '1';

module.exports = {
  dependencies: {
    // Background downloader not used on TV devices (and mmkv breaks armeabi-v7a builds)
    ...(isTV && {
      '@kesha-antonov/react-native-background-downloader': {
        platforms: {
          android: null,
          ios: null,
        },
      },
    }),
  },
};
