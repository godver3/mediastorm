import AsyncStorage from "@react-native-async-storage/async-storage";
import { Platform } from "react-native";
import { APP_VERSION } from "@/version";

const CLIENT_ID_KEY = "strmr.clientId";

let cachedClientId: string | null = null;

/**
 * Generate a UUID-like string without requiring native crypto modules.
 * Uses Math.random() which is sufficient for device identification purposes.
 */
function generateId(): string {
  const timestamp = Date.now().toString(16);
  const randomPart = () =>
    Math.floor(Math.random() * 0x10000)
      .toString(16)
      .padStart(4, "0");

  return `${timestamp}-${randomPart()}-${randomPart()}-${randomPart()}-${randomPart()}${randomPart()}${randomPart()}`;
}

/**
 * Get or generate a unique client ID for this device.
 * The ID is stored in AsyncStorage and persists across app restarts.
 */
export async function getClientId(): Promise<string> {
  if (cachedClientId) {
    return cachedClientId;
  }

  try {
    let clientId = await AsyncStorage.getItem(CLIENT_ID_KEY);
    if (!clientId) {
      clientId = generateId();
      await AsyncStorage.setItem(CLIENT_ID_KEY, clientId);
    }
    cachedClientId = clientId;
    return clientId;
  } catch (error) {
    // Fallback to a session-only ID if storage fails
    if (!cachedClientId) {
      cachedClientId = generateId();
    }
    return cachedClientId;
  }
}

/**
 * Get device information for client registration.
 * Returns device type and OS based on platform detection.
 */
export function getDeviceInfo(): { deviceType: string; os: string } {
  const isTV = Platform.isTV;
  const deviceType = isTV
    ? Platform.OS === "ios"
      ? "Apple TV"
      : "Android TV"
    : Platform.OS === "ios"
      ? "iPhone"
      : "Android Phone";

  const os =
    Platform.OS === "ios" ? (isTV ? "tvOS" : "iOS") : "Android";

  return { deviceType, os };
}

/**
 * Get the full client registration payload for the backend.
 */
export async function getClientRegistrationPayload(): Promise<{
  id: string;
  deviceType: string;
  os: string;
  appVersion: string;
}> {
  const id = await getClientId();
  const { deviceType, os } = getDeviceInfo();

  return {
    id,
    deviceType,
    os,
    appVersion: APP_VERSION,
  };
}
