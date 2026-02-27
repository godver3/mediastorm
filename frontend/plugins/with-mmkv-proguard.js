const { withDangerousMod } = require('@expo/config-plugins');
const fs = require('fs');
const path = require('path');

/**
 * Adds a ProGuard dontwarn rule for MMKV. The background downloader is
 * autolinked on all builds and references MMKV via compileOnly. On TV builds
 * where MMKV is excluded, R8 fails without this rule.
 */
const withMmkvProguard = (config) => {
  return withDangerousMod(config, [
    'android',
    async (config) => {
      const proguardPath = path.join(
        config.modRequest.projectRoot,
        'android',
        'app',
        'proguard-rules.pro'
      );

      if (!fs.existsSync(proguardPath)) {
        return config;
      }

      const contents = fs.readFileSync(proguardPath, 'utf8');
      const rule = '-dontwarn com.tencent.mmkv.MMKV';

      if (contents.includes(rule)) {
        return config;
      }

      const addition = `\n# MMKV is compileOnly in background-downloader; suppress R8 errors when\n# MMKV native lib is excluded (e.g. TV/armeabi-v7a builds).\n${rule}\n`;
      fs.writeFileSync(proguardPath, contents.trimEnd() + '\n' + addition);

      return config;
    },
  ]);
};

module.exports = withMmkvProguard;
