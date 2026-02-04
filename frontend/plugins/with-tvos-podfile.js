// Expo config plugin to configure Podfile for tvOS builds
// Ensures autolinking uses the correct platform

const { withDangerousMod } = require('@expo/config-plugins');
const fs = require('fs');
const path = require('path');

/**
 * Modify Podfile to use correct platform for autolinking
 */
const withTvOSPodfile = (config) => {
  return withDangerousMod(config, [
    'ios',
    async (config) => {
      const isTvOS = process.env.EXPO_TV === '1';
      const desiredPlatform = isTvOS ? 'tvos' : 'ios';

      console.log(`🍎 Configuring Podfile for ${desiredPlatform.toUpperCase()}...`);

      const podfilePath = path.join(config.modRequest.platformProjectRoot, 'Podfile');
      let podfileContent = fs.readFileSync(podfilePath, 'utf-8');

      // For tvOS, we need to set the deployment target to 17.0 (required by some pods)
      // The Podfile uses podfile_properties['ios.deploymentTarget'] which is 15.1
      // We need to replace the entire platform line with the correct version
      const tvosDeploymentTarget = '17.0';
      const platformRegex = /platform\s+:(?:ios|tvos),\s*[^\n]*/;

      if (platformRegex.test(podfileContent)) {
        if (isTvOS) {
          // For tvOS, set the correct deployment target directly
          podfileContent = podfileContent.replace(platformRegex, `platform :tvos, '${tvosDeploymentTarget}'`);
        } else {
          podfileContent = podfileContent.replace(platformRegex, `platform :${desiredPlatform},`);
        }
      } else {
        // Rare case: Podfile was customized and no platform exists; prepend one.
        const version = isTvOS ? ` '${tvosDeploymentTarget}'` : '';
        podfileContent = `platform :${desiredPlatform},${version}\n` + podfileContent;
      }

      // For tvOS builds, update the deployment target fix to use 17.0
      if (isTvOS) {
        const deploymentTargetFixRegex =
          /TVOS_DEPLOYMENT_TARGET'\]\.to_f < [\d.]+\s*\n\s*config\.build_settings\['TVOS_DEPLOYMENT_TARGET'\] = '[\d.]+'/g;
        if (deploymentTargetFixRegex.test(podfileContent)) {
          podfileContent = podfileContent.replace(
            deploymentTargetFixRegex,
            `TVOS_DEPLOYMENT_TARGET'].to_f < ${tvosDeploymentTarget}\n          config.build_settings['TVOS_DEPLOYMENT_TARGET'] = '${tvosDeploymentTarget}'`,
          );
          console.log(`✅ Updated post_install deployment target fix to ${tvosDeploymentTarget}`);
        }
      }

      // Note: Keep autolinking platform as 'ios' because expo-modules-autolinking
      // doesn't recognize 'tvos' as a separate platform. The tvOS build is handled
      // solely via the Podfile platform declaration above.

      fs.writeFileSync(podfilePath, podfileContent, 'utf-8');
      console.log(`✅ Podfile platform set to :${desiredPlatform}`);

      return config;
    },
  ]);
};

module.exports = withTvOSPodfile;
