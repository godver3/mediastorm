const { withDangerousMod } = require('@expo/config-plugins');
const fs = require('fs');
const path = require('path');
const os = require('os');

/**
 * Add MPV player support for Android
 * - Adds Maven repository for libmpv (jitpack)
 * - Adds module to settings.gradle
 * - Adds module dependency to app/build.gradle
 * - Registers MpvPackage in MainApplication
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
        console.warn('[MpvPlayer] build.gradle not found');
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
          console.log('[MpvPlayer] Added jitpack repository to build.gradle');
        }
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
        console.warn('[MpvPlayer] settings.gradle not found');
        return config;
      }

      let content = fs.readFileSync(settingsGradlePath, 'utf-8');

      // Add module include if not present
      if (!content.includes("':mpv-player'")) {
        content += `\n\ninclude ':mpv-player'\nproject(':mpv-player').projectDir = new File(rootProject.projectDir, '../modules/mpv-player/android')\n`;
        fs.writeFileSync(settingsGradlePath, content);
        console.log('[MpvPlayer] Added mpv-player module to settings.gradle');
      }

      return config;
    },
  ]);
};

const withMpvAppDependency = (config) => {
  return withDangerousMod(config, [
    'android',
    async (config) => {
      const appBuildGradlePath = path.join(
        config.modRequest.platformProjectRoot,
        'app',
        'build.gradle'
      );

      if (!fs.existsSync(appBuildGradlePath)) {
        console.warn('[MpvPlayer] app/build.gradle not found');
        return config;
      }

      let content = fs.readFileSync(appBuildGradlePath, 'utf-8');

      // Add project dependency if not present
      if (!content.includes("project(':mpv-player')")) {
        // Find dependencies { ... } block and add our dependency
        const depsMatch = content.match(/dependencies\s*\{/);
        if (depsMatch) {
          const insertPoint = content.indexOf(depsMatch[0]) + depsMatch[0].length;
          const depLine = `\n    implementation project(':mpv-player')`;
          content = content.slice(0, insertPoint) + depLine + content.slice(insertPoint);
          fs.writeFileSync(appBuildGradlePath, content);
          console.log('[MpvPlayer] Added mpv-player dependency to app/build.gradle');
        }
      }

      return config;
    },
  ]);
};

const withMpvMainApplication = (config) => {
  return withDangerousMod(config, [
    'android',
    async (config) => {
      // Find MainApplication.kt
      const mainAppPath = path.join(
        config.modRequest.platformProjectRoot,
        'app',
        'src',
        'main',
        'java',
        'com',
        'strmr',
        'app',
        'MainApplication.kt'
      );

      if (!fs.existsSync(mainAppPath)) {
        console.warn('[MpvPlayer] MainApplication.kt not found');
        return config;
      }

      let content = fs.readFileSync(mainAppPath, 'utf-8');

      // Add import if not present
      if (!content.includes('import com.strmr.mpvplayer.MpvPackage')) {
        // Find the last import statement and add after it
        const importMatch = content.match(/^import .+$/m);
        if (importMatch) {
          // Find all imports and insert after the last one
          const lines = content.split('\n');
          let lastImportIndex = -1;
          for (let i = 0; i < lines.length; i++) {
            if (lines[i].startsWith('import ')) {
              lastImportIndex = i;
            }
          }
          if (lastImportIndex >= 0) {
            lines.splice(lastImportIndex + 1, 0, 'import com.strmr.mpvplayer.MpvPackage');
            content = lines.join('\n');
          }
        }
      }

      // Add package to getPackages() if not present
      if (!content.includes('MpvPackage()')) {
        // Find the comment about manually adding packages
        const packagesMatch = content.match(/\/\/ Packages that cannot be autolinked yet can be added manually here/);
        if (packagesMatch) {
          content = content.replace(
            /\/\/ Packages that cannot be autolinked yet can be added manually here, for example:\s*\n\s*\/\/ add\(MyReactNativePackage\(\)\)/,
            `// Packages that cannot be autolinked yet can be added manually here:\n              add(MpvPackage())`
          );
        }
      }

      fs.writeFileSync(mainAppPath, content);
      console.log('[MpvPlayer] Updated MainApplication.kt with MpvPackage');

      return config;
    },
  ]);
};

const withLocalProperties = (config) => {
  return withDangerousMod(config, [
    'android',
    async (config) => {
      const localPropsPath = path.join(
        config.modRequest.platformProjectRoot,
        'local.properties'
      );

      // Only create if it doesn't exist and ANDROID_HOME isn't set
      if (!fs.existsSync(localPropsPath) && !process.env.ANDROID_HOME) {
        const sdkPath = path.join(os.homedir(), 'Library', 'Android', 'sdk');
        if (fs.existsSync(sdkPath)) {
          fs.writeFileSync(localPropsPath, `sdk.dir=${sdkPath}\n`);
          console.log('[MpvPlayer] Created local.properties with SDK path');
        }
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

  config = withLocalProperties(config);
  config = withMpvRepositories(config);
  config = withMpvSettingsGradle(config);
  config = withMpvAppDependency(config);
  config = withMpvMainApplication(config);
  return config;
};

module.exports = withMpvPlayer;
