const { withDangerousMod } = require('@expo/config-plugins');
const fs = require('fs');
const path = require('path');

/**
 * Add KSPlayer support for iOS/tvOS
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
        console.warn('⚠️ [KSPlayer] Podfile not found');
        return config;
      }

      let podfileContent = fs.readFileSync(podfilePath, 'utf-8');

      // Check if KSPlayer is already added
      if (podfileContent.includes("pod 'KSPlayer'")) {
        console.log('ℹ️ [KSPlayer] Pods already present in Podfile');
        // Still add the italic patch
        addItalicPatch(podfilePath);
        return config;
      }

      // Find the target block and add our pods
      const targetMatch = podfileContent.match(/(target\s+['"][^'"]+['"]\s+do)/);
      if (targetMatch) {
        const insertPoint = podfileContent.indexOf(targetMatch[0]) + targetMatch[0].length;

        // KSPlayer and its dependencies from git (with modular_headers for Swift compatibility)
        const podLines = `
  # KSPlayer - Native video player with FFmpeg support
  # GPL licensed - requires open-sourcing your project or purchasing LGPL license
  pod 'KSPlayer', :git => 'https://github.com/kingslay/KSPlayer.git', :branch => 'main'
  pod 'DisplayCriteria', :git => 'https://github.com/kingslay/KSPlayer.git', :branch => 'main', :modular_headers => true
  pod 'FFmpegKit', :git => 'https://github.com/kingslay/FFmpegKit.git', :branch => 'main', :modular_headers => true
  pod 'Libass', :git => 'https://github.com/kingslay/FFmpegKit.git', :branch => 'main', :modular_headers => true

  # Local KSPlayer React Native module wrapper
  pod 'KSPlayerModule', :path => '../modules/ksplayer'
`;
        podfileContent = podfileContent.slice(0, insertPoint) + podLines + podfileContent.slice(insertPoint);

        fs.writeFileSync(podfilePath, podfileContent);
        console.log('✅ [KSPlayer] Added pods to Podfile');
      } else {
        console.warn('⚠️ [KSPlayer] Could not find target block in Podfile');
      }

      // Add post_install hook to patch KSPlayer italic obliqueness
      addItalicPatch(podfilePath);

      return config;
    },
  ]);
};

/**
 * Add post_install hook to fix KSPlayer italic text being too slanted.
 * KSPlayer uses .obliqueness = 1 which is ~45 degrees - we reduce to 0.15
 */
function addItalicPatch(podfilePath) {
  let content = fs.readFileSync(podfilePath, 'utf-8');

  // Check if patch is already present
  if (content.includes('Patch KSPlayer italic')) {
    console.log('ℹ️ [KSPlayer] Italic patch already present');
    return;
  }

  const patchCode = `
    # Patch KSPlayer italic obliqueness (1.0 is ~45 degrees, reduce to 0.15)
    ksplayer_file = "#{installer.sandbox.root}/KSPlayer/Sources/KSPlayer/Subtitle/KSParseProtocol.swift"
    if File.exist?(ksplayer_file)
      text = File.read(ksplayer_file)
      modified = false
      if text.include?('attributes[.obliqueness] = scanner.scanFloat()')
        text = text.gsub('attributes[.obliqueness] = scanner.scanFloat()',
          'if let val = scanner.scanFloat(), val > 0 { attributes[.obliqueness] = 0.15 } else { attributes.removeValue(forKey: .obliqueness) }')
        modified = true
      end
      if text.include?('attributes[.obliqueness] = 1')
        text = text.gsub('attributes[.obliqueness] = 1', 'attributes[.obliqueness] = 0.15')
        modified = true
      end
      if modified
        File.write(ksplayer_file, text)
        puts "[KSPlayer] Patched italic obliqueness"
      end
    end
`;

  // Insert before the closing of post_install block
  const postInstallEnd = content.lastIndexOf('  end\nend');
  if (postInstallEnd !== -1) {
    content = content.slice(0, postInstallEnd) + patchCode + content.slice(postInstallEnd);
    fs.writeFileSync(podfilePath, content);
    console.log('✅ [KSPlayer] Added italic patch to post_install');
  } else {
    console.warn('⚠️ [KSPlayer] Could not find post_install end to add italic patch');
  }
}

module.exports = withKSPlayer;
