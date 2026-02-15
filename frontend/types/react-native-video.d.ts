import 'react-native-video';

declare module 'react-native-video' {
  interface ReactVideoProps {
    pictureInPicture?: boolean;
    preventDisplayModeSwitch?: boolean;
  }
}
