// API Configuration for mediastorm React Native App

import Constants from 'expo-constants';
import { NativeModules, Platform } from 'react-native';

const DEFAULT_PORT = 7777;
const DEFAULT_PATH = '/api';

const normaliseUrl = (url: string) => url.replace(/\/$/, '');

const getProtocol = () => {
  if (typeof window !== 'undefined' && window.location?.protocol) {
    return window.location.protocol; // 'https:' or 'http:'
  }
  return 'http:';
};

const withHttp = (hostOrUrl: string) => {
  if (!hostOrUrl) {
    return hostOrUrl;
  }

  if (/^https?:\/\//i.test(hostOrUrl)) {
    return normaliseUrl(hostOrUrl);
  }

  const protocol = getProtocol();
  return `${protocol}//${hostOrUrl}:${DEFAULT_PORT}${DEFAULT_PATH}`;
};

const getDevServerHost = () => {
  const expoHost = Constants?.expoConfig?.hostUri;
  if (expoHost) {
    return expoHost.split(':')[0];
  }

  const legacyDebuggerHost = (Constants as any)?.manifest?.debuggerHost as string | undefined;
  if (legacyDebuggerHost) {
    return legacyDebuggerHost.split(':')[0];
  }

  const scriptURL = NativeModules?.SourceCode?.scriptURL as string | undefined;
  if (scriptURL) {
    const parsed = scriptURL.split('://')[1]?.split('/')[0];
    if (parsed) {
      return parsed.split(':')[0];
    }
  }

  return undefined;
};

const getPlatformLocalhost = () => {
  if (Platform.OS === 'android') {
    return '10.0.2.2';
  }

  return 'localhost';
};

const dedupe = (values: (string | undefined)[]) => {
  const unique: string[] = [];
  for (const value of values) {
    if (!value) {
      continue;
    }

    if (!unique.includes(value)) {
      unique.push(value);
    }
  }
  return unique;
};

const buildUrlCandidates = (): string[] => {
  if (process.env.EXPO_PUBLIC_API_URL) {
    const explicit = normaliseUrl(process.env.EXPO_PUBLIC_API_URL);
    console.log('Using explicit API URL:', explicit);
    return [explicit];
  }

  if (typeof window !== 'undefined' && window.location) {
    const { hostname } = window.location;
    console.log('Web environment detected, hostname:', hostname);
    if (!hostname) {
      return ['http://localhost:7777/api'];
    }

    if (hostname === 'localhost' || hostname === '127.0.0.1' || hostname === 'docker') {
      const hosts = [hostname, 'localhost', '127.0.0.1', '172.17.0.1'];
      const urls = dedupe(hosts).map(withHttp);
      console.log('Using local web API URL candidates:', urls);
      return urls;
    }

    const url = withHttp(hostname);
    console.log('Using hostname-based API URL:', url);
    return [url];
  }

  const devHost = getDevServerHost();
  const hosts = dedupe([devHost, getPlatformLocalhost(), 'localhost', '127.0.0.1', '172.17.0.1']);

  const urls = hosts.map(withHttp);
  console.log('Using React Native API URL candidates:', urls);
  return urls;
};

const getBaseUrl = () => buildUrlCandidates()[0];
const getFallbackUrls = () => buildUrlCandidates().slice(1);

// Default API configuration
export const API_CONFIG = {
  BASE_URL: getBaseUrl(),
  FALLBACK_URLS: getFallbackUrls(),
  TIMEOUT: 10000,
  MAX_RETRIES: 3,
  RETRY_DELAY: 1000,
};

export const getApiConfig = () => ({
  ...API_CONFIG,
  BASE_URL: getBaseUrl(),
  FALLBACK_URLS: getFallbackUrls(),
});
