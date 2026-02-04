//
//  KSPlayerViewManager.swift
//  ksplayer
//
//  React Native View Manager for KSPlayer
//

import Foundation
import React
import KSPlayer

@objc(KSPlayerViewManager)
class KSPlayerViewManager: RCTViewManager {

    override func view() -> UIView! {
        return KSPlayerView()
    }

    override static func requiresMainQueueSetup() -> Bool {
        return true
    }

    // MARK: - Methods

    @objc func seek(_ node: NSNumber, toTime time: NSNumber) {
        DispatchQueue.main.async {
            if let view = self.bridge.uiManager.view(forReactTag: node) as? KSPlayerView {
                view.seek(to: TimeInterval(truncating: time))
            }
        }
    }

    @objc func setAudioTrack(_ node: NSNumber, trackId: NSNumber) {
        DispatchQueue.main.async {
            if let view = self.bridge.uiManager.view(forReactTag: node) as? KSPlayerView {
                view.setAudioTrack(Int(truncating: trackId))
            }
        }
    }

    @objc func setSubtitleTrack(_ node: NSNumber, trackId: NSNumber) {
        DispatchQueue.main.async {
            if let view = self.bridge.uiManager.view(forReactTag: node) as? KSPlayerView {
                view.setSubtitleTrack(Int(truncating: trackId))
            }
        }
    }

    @objc func getTracks(_ node: NSNumber, resolve: @escaping RCTPromiseResolveBlock, reject: @escaping RCTPromiseRejectBlock) {
        DispatchQueue.main.async {
            if let view = self.bridge.uiManager.view(forReactTag: node) as? KSPlayerView {
                let tracks = view.getAvailableTracks()
                resolve(tracks)
            } else {
                reject("NO_VIEW", "KSPlayerView not found", nil)
            }
        }
    }
}
