const { withDangerousMod } = require('@expo/config-plugins');
const fs = require('fs');
const path = require('path');

/**
 * Add KSPlayer support for iOS/tvOS
 * Uses a forked KSPlayer with DV Profile 5 color fix and italic obliqueness fix.
 * KSPlayer requires:
 * - KSPlayer (main player)
 * - DisplayCriteria (display settings)
 * - FFmpegKit (FFmpeg bindings)
 * - Libass (subtitle rendering)
 */

const withKSPlayer = (config) => {
  return withDangerousMod(config, [
    'ios',
    async (config) => {
      const podfilePath = path.join(config.modRequest.platformProjectRoot, 'Podfile');

      if (!fs.existsSync(podfilePath)) {
        console.warn('[KSPlayer] Podfile not found');
        return config;
      }

      let podfileContent = fs.readFileSync(podfilePath, 'utf-8');

      // Check if KSPlayer is already added
      if (podfileContent.includes("pod 'KSPlayer'")) {
        console.log('[KSPlayer] Pods already present in Podfile');
        return config;
      }

      // Find the target block and add our pods
      const targetMatch = podfileContent.match(/(target\s+['"][^'"]+['"]\s+do)/);
      if (targetMatch) {
        const insertPoint = podfileContent.indexOf(targetMatch[0]) + targetMatch[0].length;

        // KSPlayer from our fork (DV P5 color fix + italic obliqueness fix)
        // Use absolute path so it resolves even when EAS copies project to a temp dir
        const ksplayerAbsPath = path.resolve(require('os').homedir(), 'ksplayer');
        const podLines = `
  # KSPlayer - Native video player with FFmpeg support (forked with DV P5 + italic fixes)
  pod 'KSPlayer', :path => '${ksplayerAbsPath}'
  pod 'DisplayCriteria', :path => '${ksplayerAbsPath}', :modular_headers => true
  pod 'FFmpegKit', :git => 'https://github.com/kingslay/FFmpegKit.git', :branch => 'main', :modular_headers => true
  pod 'Libass', :git => 'https://github.com/kingslay/FFmpegKit.git', :branch => 'main', :modular_headers => true

  # Local KSPlayer React Native module wrapper
  pod 'KSPlayerModule', :path => '../modules/ksplayer'
`;
        podfileContent = podfileContent.slice(0, insertPoint) + podLines + podfileContent.slice(insertPoint);

        fs.writeFileSync(podfilePath, podfileContent);
        console.log('[KSPlayer] Added pods to Podfile');
      } else {
        console.warn('[KSPlayer] Could not find target block in Podfile');
      }

      return config;
    },
  ]);
};

module.exports = withKSPlayer;
