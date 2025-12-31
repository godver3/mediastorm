const { withAndroidManifest, withDangerousMod } = require('@expo/config-plugins');
const fs = require('fs');
const path = require('path');

/**
 * Add Picture-in-Picture support for Android
 * - Modifies AndroidManifest.xml to enable PiP on MainActivity
 * - Modifies MainActivity to auto-enter PiP when user leaves app
 */

const withPipManifest = (config) => {
  return withAndroidManifest(config, (config) => {
    const manifest = config.modResults.manifest;
    const application = manifest.application?.[0];

    if (!application?.activity) {
      console.warn('⚠️ [AndroidPiP] No activities found in manifest');
      return config;
    }

    const mainActivity = application.activity.find(
      (activity) => activity.$?.['android:name'] === '.MainActivity'
    );

    if (!mainActivity) {
      console.warn('⚠️ [AndroidPiP] MainActivity not found');
      return config;
    }

    // Add PiP support
    mainActivity.$['android:supportsPictureInPicture'] = 'true';

    // Add required configChanges for PiP
    const existingConfigChanges = mainActivity.$['android:configChanges'] || '';
    const requiredChanges = ['smallestScreenSize', 'screenLayout'];

    let configChanges = existingConfigChanges.split('|').filter(Boolean);
    for (const change of requiredChanges) {
      if (!configChanges.includes(change)) {
        configChanges.push(change);
      }
    }
    mainActivity.$['android:configChanges'] = configChanges.join('|');

    console.log('✅ [AndroidPiP] Added PiP support to AndroidManifest.xml');
    return config;
  });
};

const withPipMainActivity = (config) => {
  return withDangerousMod(config, [
    'android',
    async (config) => {
      const mainActivityPath = path.join(
        config.modRequest.platformProjectRoot,
        'app/src/main/java/com/strmr/app/MainActivity.kt'
      );

      if (!fs.existsSync(mainActivityPath)) {
        console.warn('⚠️ [AndroidPiP] MainActivity.kt not found at:', mainActivityPath);
        return config;
      }

      let content = fs.readFileSync(mainActivityPath, 'utf-8');

      // Check if already modified
      if (content.includes('onUserLeaveHint')) {
        console.log('ℹ️ [AndroidPiP] MainActivity already has PiP code');
        return config;
      }

      // Add required imports
      const importsToAdd = [
        'import android.app.PictureInPictureParams',
        'import android.os.Build',
        'import android.util.Rational',
      ];

      for (const importLine of importsToAdd) {
        if (!content.includes(importLine)) {
          // Add after the last import statement
          const lastImportMatch = content.match(/^import .+$/gm);
          if (lastImportMatch) {
            const lastImport = lastImportMatch[lastImportMatch.length - 1];
            content = content.replace(lastImport, `${lastImport}\n${importLine}`);
          }
        }
      }

      // Add onUserLeaveHint override
      const pipCode = `
  override fun onUserLeaveHint() {
    super.onUserLeaveHint()
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
      try {
        val params = PictureInPictureParams.Builder()
          .setAspectRatio(Rational(16, 9))
          .build()
        enterPictureInPictureMode(params)
      } catch (e: Exception) {
        // PiP not available or failed
      }
    }
  }
`;

      // Find the class body and add before the last closing brace
      const classMatch = content.match(/class MainActivity[^{]*\{/);
      if (classMatch) {
        // Find the last closing brace of the class
        const classStart = content.indexOf(classMatch[0]);
        let braceCount = 0;
        let classEnd = -1;

        for (let i = classStart; i < content.length; i++) {
          if (content[i] === '{') braceCount++;
          if (content[i] === '}') {
            braceCount--;
            if (braceCount === 0) {
              classEnd = i;
              break;
            }
          }
        }

        if (classEnd > 0) {
          content = content.slice(0, classEnd) + pipCode + content.slice(classEnd);
        }
      }

      fs.writeFileSync(mainActivityPath, content);
      console.log('✅ [AndroidPiP] Added onUserLeaveHint to MainActivity.kt');

      return config;
    },
  ]);
};

const withAndroidPip = (config) => {
  // Skip for TV builds - TV has different PiP behavior
  if (process.env.EXPO_TV === '1') {
    console.log('ℹ️ [AndroidPiP] Skipping PiP setup for TV build');
    return config;
  }

  config = withPipManifest(config);
  config = withPipMainActivity(config);
  return config;
};

module.exports = withAndroidPip;
