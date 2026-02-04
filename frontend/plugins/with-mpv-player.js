const { withDangerousMod, withGradleProperties } = require('@expo/config-plugins');
const fs = require('fs');
const path = require('path');

/**
 * Add MPV player support for Android
 * - Adds Maven repository for libmpv
 * - Adds module to settings.gradle
 */

const withMpvRepositories = (config) => {
  return withDangerousMod(config, [
    'android',
    async (config) => {
      const buildGradlePath = path.join(
        config.modRequest.platformProjectRoot,
        'build.gradle'
      );

      if (!fs.existsSync(buildGradlePath)) {
        console.warn('⚠️ [MpvPlayer] build.gradle not found');
        return config;
      }

      let content = fs.readFileSync(buildGradlePath, 'utf-8');

      // Add jitpack repository for libmpv if not present
      if (!content.includes('jitpack.io')) {
        // Find allprojects { repositories { ... } } block
        const repoMatch = content.match(/(allprojects\s*\{\s*repositories\s*\{)/);
        if (repoMatch) {
          const insertPoint = content.indexOf(repoMatch[0]) + repoMatch[0].length;
          const repoLine = `\n        maven { url 'https://jitpack.io' }`;
          content = content.slice(0, insertPoint) + repoLine + content.slice(insertPoint);
          fs.writeFileSync(buildGradlePath, content);
          console.log('✅ [MpvPlayer] Added jitpack repository to build.gradle');
        }
      } else {
        console.log('ℹ️ [MpvPlayer] jitpack repository already present');
      }

      return config;
    },
  ]);
};

const withMpvSettingsGradle = (config) => {
  return withDangerousMod(config, [
    'android',
    async (config) => {
      const settingsGradlePath = path.join(
        config.modRequest.platformProjectRoot,
        'settings.gradle'
      );

      if (!fs.existsSync(settingsGradlePath)) {
        console.warn('⚠️ [MpvPlayer] settings.gradle not found');
        return config;
      }

      let content = fs.readFileSync(settingsGradlePath, 'utf-8');

      // Add module include if not present
      if (!content.includes('mpv-player')) {
        content += `\n\ninclude ':mpv-player'\nproject(':mpv-player').projectDir = new File(rootProject.projectDir, '../modules/mpv-player/android')\n`;
        fs.writeFileSync(settingsGradlePath, content);
        console.log('✅ [MpvPlayer] Added mpv-player module to settings.gradle');
      } else {
        console.log('ℹ️ [MpvPlayer] mpv-player module already in settings.gradle');
      }

      return config;
    },
  ]);
};

const withMpvPlayer = (config) => {
  // Only add for Android
  if (process.env.EXPO_TV === '1' && process.platform === 'darwin') {
    // Skip for tvOS builds
    return config;
  }

  config = withMpvRepositories(config);
  config = withMpvSettingsGradle(config);
  return config;
};

module.exports = withMpvPlayer;
