// Expo config plugin to clean up Info.plist for tvOS builds
// Removes iOS-specific keys that cause crashes on tvOS

const { withInfoPlist } = require('@expo/config-plugins');

/**
 * Remove iOS-specific keys from tvOS builds
 */
const withTvOSInfoPlist = (config) => {
  return withInfoPlist(config, (config) => {
    const infoPlist = config.modResults;

    // Check if this is a tvOS build
    const isTvOS = process.env.EXPO_TV === '1';

    if (isTvOS) {
      console.log('🍎 Cleaning up Info.plist for tvOS build...');

      // Keep LSApplicationQueriesSchemes - tvOS supports URL schemes for external players like Infuse

      // Set LSRequiresIPhoneOS to false for tvOS
      if (infoPlist.LSRequiresIPhoneOS) {
        console.log('  ✅ Setting LSRequiresIPhoneOS to false');
        infoPlist.LSRequiresIPhoneOS = false;
      }

      // Remove iPhone-specific UI orientations
      if (infoPlist.UISupportedInterfaceOrientations) {
        console.log('  ✅ Removing iPhone-specific interface orientations');
        delete infoPlist.UISupportedInterfaceOrientations;
      }

      // Remove status bar settings (tvOS doesn't have status bar)
      if (infoPlist.UIStatusBarStyle) {
        console.log('  ✅ Removing UIStatusBarStyle (not applicable to tvOS)');
        delete infoPlist.UIStatusBarStyle;
      }

      if (infoPlist.UIViewControllerBasedStatusBarAppearance !== undefined) {
        console.log('  ✅ Removing UIViewControllerBasedStatusBarAppearance');
        delete infoPlist.UIViewControllerBasedStatusBarAppearance;
      }

      // Ensure UIUserInterfaceStyle is set for dark mode support
      if (!infoPlist.UIUserInterfaceStyle) {
        console.log('  ✅ Setting UIUserInterfaceStyle to Automatic');
        infoPlist.UIUserInterfaceStyle = 'Automatic';
      }

      console.log('✅ tvOS Info.plist cleanup complete');
    }

    return config;
  });
};

module.exports = withTvOSInfoPlist;
