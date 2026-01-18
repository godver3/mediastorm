const { withDangerousMod } = require('@expo/config-plugins');
const fs = require('fs');
const path = require('path');

/**
 * Expo config plugin to preserve ExoPlayer resources during Android resource shrinking.
 * Without this, enableShrinkResourcesInReleaseBuilds will strip ExoPlayer drawables
 * like exo_icon_fullscreen_enter, causing build failures.
 *
 * Creates res/raw/keep.xml to tell R8/ProGuard to keep ExoPlayer resources.
 */
const withExoplayerKeepResources = (config) => {
  return withDangerousMod(config, [
    'android',
    async (config) => {
      const projectRoot = config.modRequest.projectRoot;
      const rawDir = path.join(
        projectRoot,
        'android',
        'app',
        'src',
        'main',
        'res',
        'raw'
      );

      // Create the raw directory if it doesn't exist
      if (!fs.existsSync(rawDir)) {
        fs.mkdirSync(rawDir, { recursive: true });
      }

      // Create keep.xml to preserve ExoPlayer resources
      const keepXml = `<?xml version="1.0" encoding="utf-8"?>
<resources xmlns:tools="http://schemas.android.com/tools"
    tools:keep="@drawable/exo_*,@string/exo_*,@style/ExoMediaButton*,@style/ExoStyledControls*" />
`;

      const keepPath = path.join(rawDir, 'keep.xml');
      fs.writeFileSync(keepPath, keepXml);
      console.log('[with-exoplayer-keep-resources] Created keep.xml to preserve ExoPlayer resources');

      return config;
    },
  ]);
};

module.exports = withExoplayerKeepResources;
