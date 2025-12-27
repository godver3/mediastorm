import React from 'react';
import { Image, View, StyleSheet, type ViewStyle } from 'react-native';

interface QRCodeProps {
  value: string;
  size?: number;
  style?: ViewStyle;
}

/**
 * Simple QR code component using qrserver.com API.
 * No external dependencies required.
 */
export function QRCode({ value, size = 200, style }: QRCodeProps) {
  const encodedValue = encodeURIComponent(value);
  const qrUrl = `https://api.qrserver.com/v1/create-qr-code/?size=${size}x${size}&data=${encodedValue}&bgcolor=ffffff&color=000000&margin=10`;

  return (
    <View style={[styles.container, { width: size, height: size }, style]}>
      <Image
        source={{ uri: qrUrl }}
        style={{ width: size, height: size }}
        resizeMode="contain"
      />
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    backgroundColor: '#ffffff',
    borderRadius: 8,
    overflow: 'hidden',
  },
});

export default QRCode;
