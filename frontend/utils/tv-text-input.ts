import { Platform, TextInput } from 'react-native';

/**
 * Pre-focus a TextInput when its wrapper receives spatial navigation focus.
 *
 * On Android TV, the IME only opens when the native D-pad select key event
 * reaches an already-focused TextInput. By giving the TextInput native focus
 * when the user navigates to it (onFocus), the subsequent select press
 * triggers the IME in a single step.
 *
 * A small delay allows any conditional `editable` state to commit first.
 *
 * On tvOS and other platforms this is a no-op — the keyboard opens via
 * .focus() in the onSelect handler instead.
 *
 * Usage: <SpatialNavigationFocusableView onFocus={() => prefocusTextInputTV(ref)} ...>
 *        or <Pressable onFocus={() => prefocusTextInputTV(ref)} ...>
 */
export function prefocusTextInputTV(ref: React.RefObject<TextInput | null>): void {
  if (Platform.OS === 'android' && Platform.isTV) {
    setTimeout(() => {
      ref.current?.focus();
    }, 50);
  }
}

/**
 * Focus a TextInput for TV text entry (use in onSelect / onPress handlers).
 * On tvOS this immediately opens the keyboard overlay.
 * On Android TV this is a fallback — the primary mechanism is prefocusTextInputTV.
 */
export function focusTextInputTV(ref: React.RefObject<TextInput | null>): void {
  if (!ref.current) return;
  ref.current.focus();
}
