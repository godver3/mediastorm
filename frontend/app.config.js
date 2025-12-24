const fs = require('fs');
const path = require('path');

// Read version from frontend/version.txt
const getVersion = () => {
  try {
    const versionPath = path.join(__dirname, 'version.txt');
    return fs.readFileSync(versionPath, 'utf8').trim();
  } catch {
    return '1.0.0'; // fallback
  }
};

module.exports = ({ config }) => {
  const isTV = process.env.EXPO_TV === '1';
  const appVersion = getVersion();

  const plugins = [
    'expo-router',
    './plugins/with-now-playing-manager',
    [
      'expo-build-properties',
      {
        android: {
          minSdkVersion: 26,
          usesCleartextTraffic: true,
        },
        ios: {
          deploymentTarget: '15.1',
        },
        tvos: {
          deploymentTarget: '17.0',
        },
      },
    ],
    [
      'react-native-video',
      {
        enableNotificationControls: true,
        enableBackgroundAudio: true,
        enableADSExtension: false,
        enableCacheExtension: false,
        androidExtensions: {
          useExoplayerRtsp: false,
          useExoplayerSmoothStreaming: true,
          useExoplayerHls: true,
          useExoplayerDash: false,
        },
      },
    ],
    'expo-video',
  ];

  // Add dev-client for non-TV builds only (tvOS doesn't support it well)
  if (!isTV) {
    plugins.push('expo-dev-client');
  }

  if (isTV) {
    plugins.unshift('./plugins/with-tvos-info-plist');
    // Temporarily disable Xcode config plugin to avoid corruption
    // plugins.splice(1, 0, "./plugins/with-tvos-xcode-config");
    plugins.splice(1, 0, './plugins/with-tvos-podfile');
    plugins.splice(2, 0, [
      '@react-native-tvos/config-tv',
      {
        androidTVBanner: './assets/tv_icons/icon-400x240.png',
        appleTVImages: {
          icon: './assets/tv_icons/icon-1280x768.png',
          iconSmall: './assets/tv_icons/icon-400x240.png',
          iconSmall2x: './assets/tv_icons/icon-800x480.png',
          topShelf: './assets/tv_icons/icon-1920x720.png',
          topShelf2x: './assets/tv_icons/icon-1920x720.png',
          topShelfWide: './assets/tv_icons/icon-1920x720.png',
          topShelfWide2x: './assets/tv_icons/icon-1920x720.png',
        },
      },
    ]);
  }

  return {
    ...config,
    expo: {
      plugins,
      experiments: {
        typedRoutes: true,
      },
      version: appVersion,
      name: 'strmr',
      slug: 'strmr',
      scheme: 'com.strmr.app',
      icon: './assets/ios_icons/icon-1024.png',
      web: {
        favicon: './web_icons/favicon-32x32.png',
        bundler: 'metro',
        manifest: './public/manifest.json',
      },
      orientation: 'default',
      splash: {
        image: './assets/ios_icons/icon-1024.png',
        resizeMode: 'contain',
        backgroundColor: '#1a1a2e',
      },
      android: {
        package: 'com.strmr.app',
        icon: './assets/ios_icons/icon-1024.png',
        adaptiveIcon: {
          foregroundImage: './assets/ios_icons/icon-1024.png',
          backgroundColor: '#1a1a2e',
        },
        splash: {
          image: './assets/ios_icons/icon-1024.png',
          resizeMode: 'contain',
          backgroundColor: '#1a1a2e',
        },
        permissions: [],
      },
      runtimeVersion: {
        policy: 'appVersion',
      },
      updates: {
        url: 'https://u.expo.dev/1032d688-62d3-4a77-904f-3a4a3f72fcf5',
      },
      ios: {
        bundleIdentifier: isTV ? 'com.strmr.app.tv' : 'com.strmr.app',
        buildNumber: '3',
        deploymentTarget: '15.1',
        icon: './assets/ios_icons/icon-1024.png',
        infoPlist: {
          LSApplicationQueriesSchemes: ['outplayer', 'infuse'],
          ITSAppUsesNonExemptEncryption: false,
          UIBackgroundModes: ['audio'],
          ...(isTV
            ? {
                UIUserInterfaceStyle: 'Automatic',
              }
            : {
                UISupportedInterfaceOrientations: [
                  'UIInterfaceOrientationPortrait',
                  'UIInterfaceOrientationLandscapeLeft',
                  'UIInterfaceOrientationLandscapeRight',
                ],
              }),
        },
      },
      tvos: {
        bundleIdentifier: 'com.strmr.app.tv',
        deploymentTarget: '17.0',
        infoPlist: {
          LSApplicationQueriesSchemes: ['outplayer', 'infuse'],
          ITSAppUsesNonExemptEncryption: false,
          UIUserInterfaceStyle: 'Automatic',
          UIBackgroundModes: ['audio'],
        },
      },
      newArchEnabled: true,
      extra: {
        router: {},
        eas: {
          projectId: '1032d688-62d3-4a77-904f-3a4a3f72fcf5',
        },
      },
    },
  };
};
