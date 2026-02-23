const fs = require('fs');
const path = require('path');

// Read version from frontend/version.ts and truncate patch to 0 for runtime version
// e.g., '1.1.2' -> '1.1.0' (patch versions are OTA updates, major.minor are native builds)
const getVersion = () => {
  try {
    const versionPath = path.join(__dirname, 'version.ts');
    const content = fs.readFileSync(versionPath, 'utf8');
    const match = content.match(/APP_VERSION\s*=\s*['"](\d+)\.(\d+)\.\d+['"]/);
    if (match) {
      return `${match[1]}.${match[2]}.0`;
    }
    return '1.0.0';
  } catch {
    return '1.0.0'; // fallback
  }
};

module.exports = ({ config }) => {
  const isTV = process.env.EXPO_TV === '1';
  const appVersion = getVersion();

  const plugins = [
    'expo-router',
    'expo-web-browser',
    './plugins/with-android-pip',
    './plugins/with-mpv-player', // MPV native player for Android
    './plugins/with-ksplayer', // KSPlayer native player for iOS/tvOS
    './plugins/with-large-heap', // Increase Android heap limit for video playback
    './plugins/with-exoplayer-keep-resources', // Preserve ExoPlayer resources during shrinking
    // Background downloader not used on TV devices (and mmkv breaks armeabi-v7a builds)
    ...(!isTV ? ['@kesha-antonov/react-native-background-downloader'] : []),
    [
      'expo-build-properties',
      {
        android: {
          minSdkVersion: 26,
          targetSdkVersion: 34,
          usesCleartextTraffic: true,
          // Build optimizations for smaller APK and better performance on low-end devices
          enableProguardInReleaseBuilds: true,
          enableShrinkResourcesInReleaseBuilds: true,
          // Only build 64-bit ABIs (mmkv doesn't support armeabi-v7a)
          reactNativeArchitectures: ['arm64-v8a', 'x86_64'],
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
      userInterfaceStyle: 'automatic',
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
        enabled: true,
        url: 'https://u.expo.dev/1032d688-62d3-4a77-904f-3a4a3f72fcf5',
        checkAutomatically: 'ON_LOAD',
        requestHeaders: {
          'expo-channel-name': 'production',
        },
      },
      ios: {
        bundleIdentifier: isTV ? 'com.strmr.app.tv' : 'com.strmr.app',
        buildNumber: '3',
        appleTeamId: 'C98FZL89C9',
        deploymentTarget: '15.1',
        supportsTablet: true,
        icon: './assets/ios_icons/icon-1024.png',
        entitlements: {
          'com.apple.developer.networking.wifi-info': true,
        },
        infoPlist: {
          LSApplicationQueriesSchemes: ['outplayer', 'infuse'],
          ITSAppUsesNonExemptEncryption: false,
          UIBackgroundModes: ['audio'],
          NSLocalNetworkUsageDescription:
            'strmr needs to connect to your media server on your local network.',
          NSLocationWhenInUseUsageDescription:
            'strmr uses your location to detect your WiFi network and automatically switch between home and remote server URLs.',
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
        appleTeamId: 'C98FZL89C9',
        deploymentTarget: '17.0',
        infoPlist: {
          LSApplicationQueriesSchemes: ['outplayer', 'infuse'],
          ITSAppUsesNonExemptEncryption: false,
          UIUserInterfaceStyle: 'Automatic',
          UIBackgroundModes: ['audio'],
          NSLocalNetworkUsageDescription:
            'strmr needs to connect to your media server on your local network.',
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
