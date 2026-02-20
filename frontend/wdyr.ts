/// <reference types="@welldone-software/why-did-you-render" />
import React from 'react';

if (__DEV__) {
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const whyDidYouRender = require('@welldone-software/why-did-you-render');
  whyDidYouRender(React, {
    // Track all pure components (React.memo, PureComponent)
    trackAllPureComponents: true,
    // Don't track hooks by default (very noisy) — enable per-component
    trackHooks: true,
    // Only log when there are unnecessary re-renders (not all re-renders)
    logOnDifferentValues: false,
    // Collapse console groups for cleaner output
    collapseGroups: true,
    // Title prefix for console logs
    titleColor: '#FF6B35',
  });
}
