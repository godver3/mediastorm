//
//  KSPlayerManager.m
//  ksplayer
//
//  React Native bridge for KSPlayer
//

#import <React/RCTViewManager.h>
#import <React/RCTBridgeModule.h>

// View Manager - exports the KSPlayerView component
@interface RCT_EXTERN_MODULE(KSPlayerViewManager, RCTViewManager)

// View properties
RCT_EXPORT_VIEW_PROPERTY(source, NSDictionary)
RCT_EXPORT_VIEW_PROPERTY(paused, BOOL)
RCT_EXPORT_VIEW_PROPERTY(volume, float)
RCT_EXPORT_VIEW_PROPERTY(rate, float)
RCT_EXPORT_VIEW_PROPERTY(audioTrack, NSInteger)
RCT_EXPORT_VIEW_PROPERTY(subtitleTrack, NSInteger)
RCT_EXPORT_VIEW_PROPERTY(subtitleStyle, NSDictionary)
RCT_EXPORT_VIEW_PROPERTY(controlsVisible, BOOL)
RCT_EXPORT_VIEW_PROPERTY(externalSubtitleUrl, NSString)

// Event callbacks
RCT_EXPORT_VIEW_PROPERTY(onLoad, RCTDirectEventBlock)
RCT_EXPORT_VIEW_PROPERTY(onProgress, RCTDirectEventBlock)
RCT_EXPORT_VIEW_PROPERTY(onEnd, RCTDirectEventBlock)
RCT_EXPORT_VIEW_PROPERTY(onError, RCTDirectEventBlock)
RCT_EXPORT_VIEW_PROPERTY(onTracksChanged, RCTDirectEventBlock)
RCT_EXPORT_VIEW_PROPERTY(onBuffering, RCTDirectEventBlock)
RCT_EXPORT_VIEW_PROPERTY(onVideoInfo, RCTDirectEventBlock)
RCT_EXPORT_VIEW_PROPERTY(onDebugLog, RCTDirectEventBlock)
RCT_EXPORT_VIEW_PROPERTY(onPipStatusChanged, RCTDirectEventBlock)

// Methods
RCT_EXTERN_METHOD(seek:(nonnull NSNumber *)node toTime:(nonnull NSNumber *)time)
RCT_EXTERN_METHOD(enterPip:(nonnull NSNumber *)node forBackground:(BOOL)forBackground)
RCT_EXTERN_METHOD(setAudioTrack:(nonnull NSNumber *)node trackId:(nonnull NSNumber *)trackId)
RCT_EXTERN_METHOD(setSubtitleTrack:(nonnull NSNumber *)node trackId:(nonnull NSNumber *)trackId)
RCT_EXTERN_METHOD(getTracks:(nonnull NSNumber *)node
                  resolve:(RCTPromiseResolveBlock)resolve
                  reject:(RCTPromiseRejectBlock)reject)

@end
