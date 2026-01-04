const { withAndroidManifest } = require('@expo/config-plugins');

/**
 * Expo config plugin to enable android:largeHeap="true" in AndroidManifest.xml
 * This increases the heap limit from ~192MB to ~384MB+ on most devices,
 * which helps prevent OOM errors during video playback.
 *
 * @see https://gist.github.com/adamblvck/317b36ab64dd55b2bd29cc9c7d4ad1b8
 */
const withLargeHeap = (config) => {
  return withAndroidManifest(config, async (config) => {
    const androidManifest = config.modResults;

    // Get the application element
    const application = androidManifest.manifest.application?.[0];

    if (application) {
      // Add largeHeap attribute
      application.$['android:largeHeap'] = 'true';
      console.log('[with-large-heap] Added android:largeHeap="true" to AndroidManifest.xml');
    }

    return config;
  });
};

module.exports = withLargeHeap;
