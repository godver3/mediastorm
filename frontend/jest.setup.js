jest.mock('react-native/src/private/devsupport/devmenu/specs/NativeDevMenu', () => ({
  show: jest.fn(),
  reload: jest.fn(),
  setProfilingEnabled: jest.fn(),
  setHotLoadingEnabled: jest.fn(),
}));

jest.mock('react-native/src/private/specs_DEPRECATED/modules/NativeSettingsManager', () => ({
  getConstants: () => ({
    userInterfaceStyle: 'dark',
    Settings: {},
  }),
  setValues: jest.fn(),
}));

