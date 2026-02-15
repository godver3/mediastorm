//
//  KSPlayerView.swift
//  ksplayer
//
//  React Native KSPlayer View
//

import Foundation
import UIKit
import SwiftUI
import KSPlayer
import React
import CoreMedia
import AVFoundation
import Combine
#if os(tvOS)
import AVKit
#endif

// MARK: - Custom KSOptions for Frame Rate Matching (based on Flixor's approach)

class StrmrKSOptions: KSOptions {
    // Callback to notify when video properties are detected
    var onVideoPropertiesDetected: ((_ refreshRate: Float, _ isDovi: Bool, _ dynamicRange: Int32) -> Void)?

    // Store detected properties
    private(set) var detectedRefreshRate: Float = 0
    private(set) var detectedIsDovi: Bool = false
    private(set) var detectedDynamicRange: Int32 = 0

    // Note: Frame rate matching via AVDisplayManager.preferredDisplayCriteria
    // is available on tvOS but requires additional SDK configuration.
    // TODO: Re-enable frame rate matching once build configuration is resolved.

    override func updateVideo(refreshRate: Float, isDovi: Bool, formatDescription: CMFormatDescription?) {
        super.updateVideo(refreshRate: refreshRate, isDovi: isDovi, formatDescription: formatDescription)

        // Store detected properties
        detectedRefreshRate = refreshRate
        detectedIsDovi = isDovi

        // Get dynamic range from format description if available
        if let formatDesc = formatDescription {
            if let extensions = CMFormatDescriptionGetExtensions(formatDesc) as? [String: Any] {
                print("[KSPlayer] Format extensions: \(extensions.keys)")
            }
        }

        print("[KSPlayer] updateVideo - refreshRate: \(refreshRate), isDovi: \(isDovi)")

        // Notify callback
        DispatchQueue.main.async { [weak self] in
            guard let self = self else { return }
            self.onVideoPropertiesDetected?(refreshRate, isDovi, self.detectedDynamicRange)
        }
    }

    /// Override to increase frame buffer capacity for high bitrate content (from Flixor)
    override func videoFrameMaxCount(fps: Float, naturalSize: CGSize, isLive: Bool) -> UInt8 {
        if isLive {
            return 8
        }
        // 4K needs more buffer frames
        if naturalSize.width >= 3840 || naturalSize.height >= 2160 {
            return 32
        } else if naturalSize.width >= 1920 || naturalSize.height >= 1080 {
            return 24
        }
        return 16
    }
}

@objc(KSPlayerView)
public class KSPlayerView: UIView {
    private var playerView: VideoPlayerView?
    private var progressTimer: Timer?
    private var subtitleRetryTimer: Timer?
    private var currentSource: NSDictionary?
    private var currentOptions: StrmrKSOptions?
    private var isPaused: Bool = true
    private var pipCancellable: AnyCancellable?
    private var pendingSeek: Double?
    private var pendingAudioTrack: Int?
    private var pendingSubtitleTrack: Int?
    private var subtitleRetryCount: Int = 0
    private var lastReportedDynamicRange: String = ""
    private var videoSourceHeight: CGFloat = 0
    private var userFontSizeMultiplier: CGFloat = 1.0

    // MARK: - React Native Event Blocks
    @objc var onLoad: RCTDirectEventBlock?
    @objc var onProgress: RCTDirectEventBlock?
    @objc var onEnd: RCTDirectEventBlock?
    @objc var onError: RCTDirectEventBlock?
    @objc var onTracksChanged: RCTDirectEventBlock?
    @objc var onBuffering: RCTDirectEventBlock?
    @objc var onVideoInfo: RCTDirectEventBlock?
    @objc var onDebugLog: RCTDirectEventBlock?
    @objc var onPipStatusChanged: RCTDirectEventBlock?

    // Helper to send debug logs to JS
    private func debugLog(_ message: String) {
        print("[KSPlayer] \(message)")
        onDebugLog?(["message": message])
    }

    // MARK: - React Native Properties

    @objc var source: NSDictionary? {
        didSet {
            setSource(source)
        }
    }

    @objc var paused: Bool = true {
        didSet {
            setPaused(paused)
        }
    }

    @objc var volume: Float = 1.0 {
        didSet {
            setVolume(volume)
        }
    }

    @objc var rate: Float = 1.0 {
        didSet {
            setPlaybackRate(rate)
        }
    }

    @objc var audioTrack: Int = -1 {
        didSet {
            print("[KSPlayer] audioTrack prop changed: \(oldValue) -> \(audioTrack)")
            if audioTrack >= 0 {
                setAudioTrack(audioTrack)
            }
        }
    }

    @objc var subtitleTrack: Int = -1 {
        didSet {
            print("[KSPlayer] subtitleTrack prop changed: \(oldValue) -> \(subtitleTrack)")
            setSubtitleTrack(subtitleTrack)
        }
    }

    @objc var subtitleStyle: NSDictionary? {
        didSet {
            applySubtitleStyle(subtitleStyle)
        }
    }

    // When controls are visible, move subtitles up so they're not hidden
    @objc var controlsVisible: Bool = false {
        didSet {
            updateSubtitlePosition()
        }
    }

    @objc var externalSubtitleUrl: NSString? {
        didSet {
            print("[KSPlayer] externalSubtitleUrl didSet: old=\(String(describing: oldValue)), new=\(String(describing: externalSubtitleUrl))")
            handleExternalSubtitleUrlChanged(externalSubtitleUrl as String?)
        }
    }

    // External subtitle state
    private var currentExternalSubtitleInfo: URLSubtitleInfo?
    private var pendingExternalSubtitleUrl: String?

    // Base bottom margin from subtitleStyle (default 50)
    private var baseBottomMargin: CGFloat = 50

    // MARK: - Initialization

    override init(frame: CGRect) {
        super.init(frame: frame)
        setupPlayer()
    }

    required init?(coder: NSCoder) {
        super.init(coder: coder)
        setupPlayer()
    }

    deinit {
        cleanup()
    }

    private func setupPlayer() {
        // Configure global KSPlayer options (based on Flixor's approach)
        KSOptions.isAutoPlay = false
        KSOptions.isAccurateSeek = true
        KSOptions.isLoopPlay = false

        #if targetEnvironment(simulator)
        // Simulator: disable hardware decode to avoid VT issues
        KSOptions.hardwareDecode = false
        KSOptions.asynchronousDecompression = false
        KSOptions.secondPlayerType = nil
        #else
        // Device: Use KSMEPlayer (FFmpeg) as primary for full codec support including DV
        // KSMEPlayer provides hardware decode via VideoToolbox with full control
        KSOptions.firstPlayerType = KSMEPlayer.self
        KSOptions.secondPlayerType = KSAVPlayer.self  // Fallback to AVPlayer if FFmpeg fails
        KSOptions.hardwareDecode = true
        KSOptions.asynchronousDecompression = true
        #endif

        // Forward KSPlayer internal debug logs to our debugLog() → onDebugLog → Metro console
        KSOptions.debugLogHandler = { [weak self] message in
            self?.debugLog(message)
        }

        print("[KSPlayer] Global settings: firstPlayer=KSMEPlayer, hwDecode=\(KSOptions.hardwareDecode), asyncDecomp=\(KSOptions.asynchronousDecompression)")

        // Create the player view
        let player = VideoPlayerView()
        player.translatesAutoresizingMaskIntoConstraints = false
        player.delegate = self

        // Hide native controls - we use our own UI
        player.toolBar.isHidden = true
        player.toolBar.timeSlider.isHidden = true
        player.navigationBar.isHidden = true
        player.topMaskView.isHidden = true
        player.bottomMaskView.isHidden = true
        player.isMaskShow = false

        // Ensure subtitle views are visible (they might be hidden by default)
        player.subtitleLabel.isHidden = false
        player.subtitleBackView.isHidden = false

        // Disable text shadow since we use a background box for readability instead
        // (matching SubtitleOverlay styling)
        player.subtitleLabel.layer.shadowOpacity = 0

        // Disable clipping so subtitles can animate above controls without being cut off
        player.clipsToBounds = false
        player.subtitleBackView.superview?.clipsToBounds = false
        player.subtitleLabel.superview?.clipsToBounds = false

        print("[KSPlayer] Subtitle views setup - label.isHidden: \(player.subtitleLabel.isHidden), backView.isHidden: \(player.subtitleBackView.isHidden)")

        addSubview(player)

        NSLayoutConstraint.activate([
            player.topAnchor.constraint(equalTo: topAnchor),
            player.bottomAnchor.constraint(equalTo: bottomAnchor),
            player.leadingAnchor.constraint(equalTo: leadingAnchor),
            player.trailingAnchor.constraint(equalTo: trailingAnchor)
        ])

        self.playerView = player
    }

    private func cleanup() {
        progressTimer?.invalidate()
        progressTimer = nil
        subtitleRetryTimer?.invalidate()
        subtitleRetryTimer = nil
        pipCancellable?.cancel()
        pipCancellable = nil

        playerView?.resetPlayer()
        playerView?.removeFromSuperview()
        playerView = nil
    }

    // Note: resetDisplayCriteria for tvOS frame rate matching removed
    // TODO: Re-enable once SDK configuration is resolved

    private func startProgressTimer() {
        progressTimer?.invalidate()
        progressTimer = Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] _ in
            self?.reportProgress()
        }
    }

    private func stopProgressTimer() {
        progressTimer?.invalidate()
        progressTimer = nil
    }

    private func reportProgress() {
        guard let player = playerView,
              let playerLayer = player.playerLayer else { return }

        let currentTime = playerLayer.player.currentPlaybackTime
        let duration = playerLayer.player.duration

        // Only require currentTime to be finite. Duration may be NaN/infinity
        // for DV P5 HLS EVENT playlists (before remux completes with EXT-X-ENDLIST).
        guard currentTime.isFinite else { return }

        onProgress?([
            "currentTime": currentTime,
            "duration": duration.isFinite ? duration : 0
        ])
    }

    // MARK: - Public API

    func setSource(_ source: NSDictionary?) {
        guard let source = source,
              let uri = source["uri"] as? String,
              let url = URL(string: uri) else {
            debugLog("setSource: invalid source - source=\(String(describing: source))")
            return
        }

        debugLog("setSource: full uri=\(uri)")
        debugLog("setSource: headers=\(source["headers"] ?? "nil")")
        if let preselectedSub = source["preselectedSubtitleTrack"] as? Int {
            debugLog("setSource: preselectedSubtitleTrack=\(preselectedSub)")
        }
        if let preselectedAudio = source["preselectedAudioTrack"] as? Int {
            debugLog("setSource: preselectedAudioTrack=\(preselectedAudio)")
        }

        // Reset state for new source
        currentSource = source
        lastReportedDynamicRange = ""
        subtitleRetryTimer?.invalidate()
        subtitleRetryTimer = nil
        subtitleRetryCount = 0
        currentExternalSubtitleInfo = nil
        pendingExternalSubtitleUrl = nil

        setupAndPlaySource(url: url, source: source)
    }

    /// Common setup and play logic extracted from setSource
    private func setupAndPlaySource(url: URL, source: NSDictionary) {
        // Use custom options class for frame rate matching support
        let options = StrmrKSOptions()
        currentOptions = options

        // Configure options based on Flixor's approach - let KSPlayer auto-detect HDR
        #if !targetEnvironment(simulator)
        // Enable hardware decode and async decompression for smooth playback
        options.hardwareDecode = true
        options.asynchronousDecompression = true
        options.syncDecodeVideo = false
        options.syncDecodeAudio = false
        options.videoAdaptable = true
        #endif

        // HDR handling: let KSPlayer auto-detect dynamic range from stream metadata
        options.destinationDynamicRange = nil

        // Performance settings
        options.preferredForwardBufferDuration = 5.0
        options.maxBufferDuration = 300.0
        options.isSecondOpen = true
        options.probesize = 50_000_000
        options.maxAnalyzeDuration = 5_000_000
        options.decoderOptions["threads"] = "0"

        // Subtitle settings - autoSelectEmbedSubtitle must be true for KSPlayer to initialize subtitle decoder
        options.autoSelectEmbedSubtitle = true

        debugLog("Options configured: hardwareDecode=\(options.hardwareDecode), asyncDecomp=\(options.asynchronousDecompression), destinationDynamicRange=\(options.destinationDynamicRange?.description ?? "auto"), autoSelectEmbedSubtitle=\(options.autoSelectEmbedSubtitle)")

        // Set up callback to detect Dolby Vision during playback
        options.onVideoPropertiesDetected = { [weak self] refreshRate, isDovi, _ in
            guard let self = self else { return }

            // If DV detected and we haven't reported it yet, update the video info
            if isDovi && self.lastReportedDynamicRange != "DolbyVision" {
                print("[KSPlayer] Dolby Vision detected via callback")
                self.lastReportedDynamicRange = "DolbyVision"
                self.onVideoInfo?([
                    "frameRate": refreshRate,
                    "dynamicRange": "DolbyVision",
                    "codec": "HEVC",
                    "hdrActive": true
                ])
            }
        }

        // Configure headers if provided
        if let headers = source["headers"] as? [String: String] {
            options.appendHeader(headers)
        }

        // Set the resource
        debugLog("Creating KSPlayerResource with URL: \(url)")
        let resource = KSPlayerResource(url: url, options: options)
        debugLog("Calling playerView.set(resource:)")
        playerView?.set(resource: resource)
        debugLog("Resource set complete, isPaused=\(isPaused)")

        // Auto-play if not paused
        if !isPaused {
            debugLog("Starting playback (isPaused was false)")
            playerView?.play()
        }
    }

    func setPaused(_ paused: Bool) {
        print("[KSPlayer] setPaused: \(paused), playerView=\(playerView != nil ? "exists" : "nil")")
        isPaused = paused
        if paused {
            playerView?.pause()
            stopProgressTimer()
        } else {
            playerView?.play()
            startProgressTimer()
        }
    }

    func setVolume(_ volume: Float) {
        guard let player = playerView?.playerLayer?.player else { return }
        player.isMuted = volume == 0
        player.playbackVolume = volume
    }

    func setPlaybackRate(_ rate: Float) {
        playerView?.playerLayer?.player.playbackRate = rate
    }

    func setAudioTrack(_ trackId: Int) {
        guard let player = playerView?.playerLayer?.player else {
            print("[KSPlayer] setAudioTrack: player not ready, storing pending trackId=\(trackId)")
            pendingAudioTrack = trackId
            return
        }
        let tracks = player.tracks(mediaType: .audio)
        print("[KSPlayer] setAudioTrack: trackId=\(trackId), available tracks=\(tracks.count)")
        if trackId >= 0 && trackId < tracks.count {
            let track = tracks[trackId]
            player.select(track: track)
            print("[KSPlayer] Selected audio track: \(track.name), language: \(track.language ?? "unknown")")
        } else {
            print("[KSPlayer] setAudioTrack: trackId \(trackId) out of range (max: \(tracks.count - 1))")
        }
    }

    func setSubtitleTrack(_ trackId: Int) {
        guard let pv = playerView else {
            print("[KSPlayer] setSubtitleTrack: playerView not ready, storing pending trackId=\(trackId)")
            pendingSubtitleTrack = trackId
            return
        }

        // Use srtControl.subtitleInfos for subtitle selection (not player.tracks)
        let subtitleInfos = pv.srtControl.subtitleInfos
        print("[KSPlayer] setSubtitleTrack: trackId=\(trackId), available subtitleInfos=\(subtitleInfos.count)")

        // If subtitles not loaded yet, store as pending and schedule retry
        if subtitleInfos.isEmpty && trackId >= 0 {
            print("[KSPlayer] setSubtitleTrack: subtitleInfos empty, storing pending trackId=\(trackId) and scheduling retry")
            pendingSubtitleTrack = trackId
            scheduleSubtitleRetry()
            return
        }

        // Clear any pending retry since we're now able to apply the selection
        subtitleRetryTimer?.invalidate()
        subtitleRetryTimer = nil
        subtitleRetryCount = 0
        pendingSubtitleTrack = nil

        if trackId < 0 {
            // Subtitles disabled - set selectedSubtitleInfo to nil
            print("[KSPlayer] Disabling subtitles via srtControl.selectedSubtitleInfo = nil")
            pv.srtControl.selectedSubtitleInfo = nil
            return
        }

        if trackId < subtitleInfos.count {
            let targetInfo = subtitleInfos[trackId]
            print("[KSPlayer] Selecting subtitle via srtControl: \(targetInfo.name)")
            pv.srtControl.selectedSubtitleInfo = targetInfo
            print("[KSPlayer] Set srtControl.selectedSubtitleInfo successfully")
        } else {
            print("[KSPlayer] setSubtitleTrack: trackId \(trackId) out of range (max: \(subtitleInfos.count - 1))")
        }
    }

    /// Schedule a retry for applying pending subtitle track selection
    /// KSPlayer's srtControl.subtitleInfos may not be populated immediately after readyToPlay
    private func scheduleSubtitleRetry() {
        // Cancel any existing retry timer
        subtitleRetryTimer?.invalidate()

        // Limit retries to prevent infinite loops (10 retries at 200ms = 2 seconds max)
        let maxRetries = 10
        if subtitleRetryCount >= maxRetries {
            print("[KSPlayer] scheduleSubtitleRetry: max retries (\(maxRetries)) reached, giving up")
            pendingSubtitleTrack = nil
            return
        }

        subtitleRetryCount += 1
        subtitleRetryTimer = Timer.scheduledTimer(withTimeInterval: 0.2, repeats: false) { [weak self] _ in
            guard let self = self, let trackId = self.pendingSubtitleTrack else { return }
            print("[KSPlayer] subtitle retry #\(self.subtitleRetryCount) for trackId=\(trackId)")
            self.setSubtitleTrack(trackId)
        }
    }

    private func applyPendingTrackSelections() {
        if let audioTrack = pendingAudioTrack {
            pendingAudioTrack = nil
            setAudioTrack(audioTrack)
        }
        if let subtitleTrack = pendingSubtitleTrack {
            // Don't clear pendingSubtitleTrack here - setSubtitleTrack handles it
            // This allows the retry mechanism to work if subtitleInfos isn't ready
            setSubtitleTrack(subtitleTrack)
        }
        if let pendingUrl = pendingExternalSubtitleUrl {
            pendingExternalSubtitleUrl = nil
            handleExternalSubtitleUrlChanged(pendingUrl)
        }
    }

    func applySubtitleStyle(_ style: NSDictionary?) {
        guard let style = style else { return }

        // Font size (multiplier, 1.0 = default)
        if let fontSize = style["fontSize"] as? Double {
            // Text subtitle scaling (via font size)
            #if os(tvOS)
            let baseSize: CGFloat = 60.0
            #else
            let baseSize: CGFloat = 24.0
            #endif
            SubtitleModel.textFontSize = baseSize * CGFloat(fontSize)
            playerView?.subtitleLabel.font = SubtitleModel.textFont
            print("[KSPlayer] Subtitle font size set to: \(SubtitleModel.textFontSize)")

            // Bitmap subtitle scaling (via image resizing in VideoPlayerView)
            userFontSizeMultiplier = CGFloat(fontSize)
            updateBitmapSubtitleScale()
        }

        // Text color (hex string like '#FFFFFF') - convert to SwiftUI Color
        if let textColor = style["textColor"] as? String {
            if let uiColor = UIColor(hexString: textColor) {
                SubtitleModel.textColor = Color(uiColor)
                // Apply to the label immediately
                playerView?.subtitleLabel.textColor = uiColor
                print("[KSPlayer] Subtitle text color set to: \(textColor)")
            }
        }

        // Background color (hex string with alpha like '#00000080') - convert to SwiftUI Color
        // This creates a box background behind the subtitle text for better readability
        if let bgColor = style["backgroundColor"] as? String {
            if let uiColor = UIColor(hexString: bgColor) {
                SubtitleModel.textBackgroundColor = Color(uiColor)
                // Apply to the backView immediately and configure box styling
                playerView?.subtitleBackView.backgroundColor = uiColor
                playerView?.subtitleBackView.layer.masksToBounds = true
                #if os(tvOS)
                playerView?.subtitleBackView.layer.cornerRadius = 6
                #else
                playerView?.subtitleBackView.layer.cornerRadius = 4
                #endif
                print("[KSPlayer] Subtitle background color set to: \(bgColor)")
            }
        }

        // Bottom margin - store base value and apply with controls offset
        // Note: TextPosition initializer is internal, so we modify the margin directly
        if let bottomMargin = style["bottomMargin"] as? Double {
            baseBottomMargin = CGFloat(bottomMargin)
            updateSubtitlePosition()
            print("[KSPlayer] Subtitle base bottom margin set to: \(bottomMargin)")
        }
    }

    func updateSubtitlePosition() {
        // Move subtitle view up when controls are visible using transform
        // KSPlayer's subtitle view uses a fixed bottom constraint, so we use transform instead
        guard let pv = playerView else {
            print("[KSPlayer] updateSubtitlePosition: playerView not ready")
            return
        }

        #if os(tvOS)
        let controlsOffset: CGFloat = controlsVisible ? -125 : 0
        #else
        let isPortrait = bounds.height > bounds.width
        let controlsOffset: CGFloat = controlsVisible ? (isPortrait ? -175 : -125) : 0
        #endif

        DispatchQueue.main.async {
            // Ensure parent views don't clip the subtitles when they move up
            // KSPlayer's internal views may have clipsToBounds enabled by default
            pv.clipsToBounds = false
            pv.subtitleBackView.superview?.clipsToBounds = false

            UIView.animate(withDuration: 0.2) {
                // Only transform subtitleBackView — subtitleLabel is a child of backView,
                // so it moves with it automatically. Transforming both causes the label
                // to shift twice (parent transform + its own).
                pv.subtitleBackView.transform = CGAffineTransform(translationX: 0, y: controlsOffset)
            }
        }
        print("[KSPlayer] Subtitle position updated: controlsVisible=\(controlsVisible), offset=\(controlsOffset)")
    }

    private func handleExternalSubtitleUrlChanged(_ urlString: String?) {
        guard let pv = playerView else {
            // Player not ready yet — store for later
            print("[KSPlayer] externalSubtitleUrl: playerView not ready, storing pending URL")
            pendingExternalSubtitleUrl = urlString
            return
        }

        pendingExternalSubtitleUrl = nil

        // Clear case: URL is nil or empty
        guard let urlString = urlString, !urlString.isEmpty else {
            if let existing = currentExternalSubtitleInfo {
                print("[KSPlayer] externalSubtitleUrl: clearing external subtitle")
                // Deselect if it's currently selected
                if pv.srtControl.selectedSubtitleInfo?.subtitleID == existing.subtitleID {
                    pv.srtControl.selectedSubtitleInfo = nil
                }
                currentExternalSubtitleInfo = nil
            }
            return
        }

        // Same URL — ensure it's selected
        if let existing = currentExternalSubtitleInfo, existing.downloadURL.absoluteString == urlString {
            if pv.srtControl.selectedSubtitleInfo?.subtitleID != existing.subtitleID {
                print("[KSPlayer] externalSubtitleUrl: re-selecting existing external subtitle")
                pv.srtControl.selectedSubtitleInfo = existing
            }
            return
        }

        // New URL — create URLSubtitleInfo, add to srtControl, select it
        guard let url = URL(string: urlString) else {
            print("[KSPlayer] externalSubtitleUrl: invalid URL: \(urlString)")
            return
        }

        print("[KSPlayer] externalSubtitleUrl: loading external subtitle from \(urlString)")
        let subtitleInfo = URLSubtitleInfo(subtitleID: "external-subtitle", name: "External Subtitle", url: url)
        pv.srtControl.addSubtitle(info: subtitleInfo)
        pv.srtControl.selectedSubtitleInfo = subtitleInfo
        currentExternalSubtitleInfo = subtitleInfo
    }

    /// Apply the user font size multiplier and video source height for bitmap subtitle scaling.
    /// Per-part resolution scaling is computed in VideoPlayerView using bitmapAuthoringHeight
    /// and videoSourceHeight to pick the correct denominator for each platform.
    private func updateBitmapSubtitleScale() {
        guard let pv = playerView else { return }
        pv.bitmapSubtitleScale = userFontSizeMultiplier
        pv.videoSourceHeight = videoSourceHeight
        print("[KSPlayer] bitmapSubtitleScale=\(userFontSizeMultiplier), videoSourceHeight=\(videoSourceHeight)")
    }

    func enterPip(forBackground: Bool = false) {
        guard let playerLayer = playerView?.playerLayer else {
            debugLog("[PiP] enterPip: playerLayer not available")
            return
        }

        let isP5 = currentOptions?.doviProfile5Detected ?? false
        let isP78 = currentOptions?.doviProfile78Detected ?? false
        debugLog("[PiP] enterPip: forBackground=\(forBackground) doviP5=\(isP5) doviP78=\(isP78)")

        // Set isPipActive on options FIRST so MetalPlayView can adjust routing
        // P7/P8: suppresses Metal DOVI → display layer shows HDR10 base layer
        // P5: triggers Metal readback → DOVI-corrected frames enqueued to display layer
        currentOptions?.isPipActive = true

        if forBackground {
            // Background path: Native willResignActive handler in KSPlayerLayer
            // already prepares DOVI frame routing and delegate BEFORE the scene
            // transitions (giving the display layer time to receive frames).
            // This JS bridge path is a safety net — it arrives after willResignActive
            // but ensures options.isPipActive stays set.
            if #available(tvOS 14.0, *) {
                guard playerLayer.player.pipController != nil else {
                    debugLog("[PiP] enterPip(bg): no pip controller, reverting")
                    currentOptions?.isPipActive = false
                    return
                }
                playerLayer.player.pipController?.delegate = playerLayer
                debugLog("[PiP] enterPip(bg): safety net — frame routing + delegate confirmed")
            }
        } else {
            // Foreground (manual) path: small delay to let display layer receive first frame
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) { [weak self] in
                guard let self = self, let playerLayer = self.playerView?.playerLayer else {
                    self?.currentOptions?.isPipActive = false
                    self?.debugLog("[PiP] enterPip: playerLayer gone after delay")
                    return
                }
                if #available(tvOS 14.0, *) {
                    let hasPipController = playerLayer.player.pipController != nil
                    let isPipPossible = playerLayer.player.pipController?.isPictureInPicturePossible ?? false
                    self.debugLog("[PiP] enterPip(fg): hasPipController=\(hasPipController) isPipPossible=\(isPipPossible)")
                    if !hasPipController || !isPipPossible {
                        self.debugLog("[PiP] enterPip(fg): PiP not possible, reverting")
                        self.currentOptions?.isPipActive = false
                        return
                    }
                }
                self.debugLog("[PiP] enterPip(fg): setting playerLayer.isPipActive = true")
                playerLayer.isPipActive = true
            }
        }
    }

    func seek(to time: Double) {
        guard let player = playerView else {
            pendingSeek = time
            return
        }

        player.seek(time: TimeInterval(time)) { [weak self] finished in
            if finished {
                self?.reportProgress()
            }
        }
    }

    func getAvailableTracks() -> [String: Any] {
        guard let player = playerView?.playerLayer?.player else {
            debugLog("getAvailableTracks: player is nil")
            return ["audioTracks": [], "subtitleTracks": []]
        }

        // Log all tracks KSPlayer knows about (all media types)
        let allAudio = player.tracks(mediaType: .audio)
        let allVideo = player.tracks(mediaType: .video)
        let allSubtitle = player.tracks(mediaType: .subtitle)
        debugLog("getAvailableTracks: player reports \(allVideo.count) video, \(allAudio.count) audio, \(allSubtitle.count) subtitle tracks")

        let audioTracks = allAudio.enumerated().map { (index, track) -> [String: Any] in
            debugLog("  audio[\(index)]: name=\(track.name), lang=\(track.language ?? track.languageCode ?? "?"), codecType=\(track.codecType), trackID=\(track.trackID)")
            return [
                "id": index,
                "type": "audio",
                "title": track.name,
                "language": track.language ?? "",
                "codec": String(track.codecType),
                "selected": track.isEnabled
            ]
        }

        // First try player.tracks() which reads from FFmpeg's asset tracks
        var subtitleTracksList = allSubtitle
        debugLog("getAvailableTracks: player.tracks(subtitle) count=\(subtitleTracksList.count)")
        for (i, track) in subtitleTracksList.enumerated() {
            debugLog("  subtitle[\(i)]: name=\(track.name), lang=\(track.language ?? track.languageCode ?? "?"), codecType=\(track.codecType), trackID=\(track.trackID), isImageSubtitle=\(track.isImageSubtitle), isEnabled=\(track.isEnabled)")
        }

        // Also log srtControl state regardless
        if let pv = playerView {
            let srtInfos = pv.srtControl.subtitleInfos
            debugLog("getAvailableTracks: srtControl.subtitleInfos count=\(srtInfos.count)")
            for (i, info) in srtInfos.enumerated() {
                let lang = (info as? MediaPlayerTrack)?.language ?? (info as? MediaPlayerTrack)?.languageCode ?? "?"
                let isImg = (info as? MediaPlayerTrack)?.isImageSubtitle ?? false
                let trackID = (info as? MediaPlayerTrack)?.trackID ?? -1
                debugLog("  srtInfo[\(i)]: name=\(info.name), lang=\(lang), isImageSubtitle=\(isImg), trackID=\(trackID), isEnabled=\(info.isEnabled)")
            }
        }

        // If player.tracks() is empty, fall back to srtControl.subtitleInfos
        // VideoPlayerView delays loading embedded subtitles by ~1 second after readyToPlay,
        // so srtControl may have tracks that player.tracks() missed or hasn't reported yet
        if subtitleTracksList.isEmpty, let pv = playerView {
            let srtInfos = pv.srtControl.subtitleInfos
            if !srtInfos.isEmpty {
                debugLog("getAvailableTracks: using srtControl fallback (\(srtInfos.count) tracks)")
                // Use srtControl infos — these include PGS/bitmap subtitle tracks
                let subtitleTracks = srtInfos.enumerated().map { (index, info) -> [String: Any] in
                    return [
                        "id": index,
                        "type": "subtitle",
                        "title": info.name,
                        "language": (info as? MediaPlayerTrack)?.language ?? "",
                        "codec": "",
                        "selected": info.isEnabled
                    ]
                }
                return [
                    "audioTracks": audioTracks,
                    "subtitleTracks": subtitleTracks
                ]
            }
        }

        let subtitleTracks = subtitleTracksList.enumerated().map { (index, track) -> [String: Any] in
            return [
                "id": index,
                "type": "subtitle",
                "title": track.name,
                "language": track.language ?? "",
                "codec": String(track.codecType),
                "selected": track.isEnabled
            ]
        }

        debugLog("getAvailableTracks: returning \(audioTracks.count) audio, \(subtitleTracks.count) subtitle tracks")
        return [
            "audioTracks": audioTracks,
            "subtitleTracks": subtitleTracks
        ]
    }

    private func reportTracks() {
        let tracks = getAvailableTracks()
        let audioCount = (tracks["audioTracks"] as? [[String: Any]])?.count ?? 0
        let subCount = (tracks["subtitleTracks"] as? [[String: Any]])?.count ?? 0
        debugLog("reportTracks: sending \(audioCount) audio, \(subCount) subtitle tracks to JS")
        onTracksChanged?(tracks)

        // Subtitles may have loaded after readyToPlay - trigger a retry attempt
        // This is in addition to the timer-based retry, for faster application when tracks load
        if pendingSubtitleTrack != nil {
            print("[KSPlayer] reportTracks: tracks reported, triggering subtitle selection retry")
            // Reset retry count since we have new information
            subtitleRetryCount = 0
            if let trackId = pendingSubtitleTrack {
                setSubtitleTrack(trackId)
            }
        }
    }

    private func reportVideoInfo(player: MediaPlayerProtocol) {
        var frameRate: Float = 0
        var dynamicRange = "SDR"
        var codecName = ""

        for track in player.tracks(mediaType: .video) {
            frameRate = track.nominalFrameRate

            // Convert FourCC codec type to readable string
            let fourCC = track.codecType
            codecName = fourCCToString(fourCC)

            // Check for Dolby Vision codecs first
            let dvCodecs = ["dvh1", "dvhe", "dva1", "dvav", "dav1"]
            if dvCodecs.contains(codecName.lowercased()) {
                dynamicRange = "DolbyVision"
            }

            // Check color primaries for HDR detection
            if let colorPrimaries = track.colorPrimaries {
                print("[KSPlayer] colorPrimaries: \(colorPrimaries)")

                if colorPrimaries.contains("2020") {
                    if dynamicRange != "DolbyVision" {
                        dynamicRange = "HDR10"
                    }
                }
            } else {
                print("[KSPlayer] colorPrimaries: nil (unspecified)")
            }

            // Check YCbCr matrix (this is critical for correct color rendering)
            if let ycbcrMatrix = track.yCbCrMatrix {
                print("[KSPlayer] ycbcrMatrix: \(ycbcrMatrix)")
            } else {
                print("[KSPlayer] ycbcrMatrix: nil (unspecified) - may cause color issues!")
            }

            // Check transfer function for HDR type
            if let transferFunction = track.transferFunction {
                print("[KSPlayer] transferFunction: \(transferFunction)")

                if transferFunction.contains("HLG") {
                    if dynamicRange != "DolbyVision" {
                        dynamicRange = "HLG"
                    }
                } else if transferFunction.contains("PQ") || transferFunction.contains("SMPTE2084") {
                    if dynamicRange != "DolbyVision" {
                        dynamicRange = "HDR10"
                    }
                }
            }

            // Check for Dolby Vision in the track name or description
            if let trackName = track.name.lowercased() as String?,
               trackName.contains("dolby") || trackName.contains("vision") || trackName.contains("dovi") {
                dynamicRange = "DolbyVision"
            }

            break // Use first video track
        }

        // Store the reported dynamic range so DV callback can update if needed
        lastReportedDynamicRange = dynamicRange

        onVideoInfo?([
            "frameRate": frameRate,
            "dynamicRange": dynamicRange,
            "codec": codecName,
            "hdrActive": dynamicRange != "SDR"
        ])
    }

    // Convert FourCC UInt32 to readable string (e.g., 0x68766331 -> "hvc1")
    private func fourCCToString(_ fourCC: FourCharCode) -> String {
        let bytes: [CChar] = [
            CChar(truncatingIfNeeded: (fourCC >> 24) & 0xFF),
            CChar(truncatingIfNeeded: (fourCC >> 16) & 0xFF),
            CChar(truncatingIfNeeded: (fourCC >> 8) & 0xFF),
            CChar(truncatingIfNeeded: fourCC & 0xFF),
            0 // null terminator
        ]
        return String(cString: bytes)
    }
}

// MARK: - PlayerControllerDelegate

extension KSPlayerView: PlayerControllerDelegate {
    public func playerController(state: KSPlayerState) {
        switch state {
        case .preparing:
            print("[KSPlayer] playerController(state: .preparing)")
            onBuffering?(["buffering": true])

        case .readyToPlay:
            print("[KSPlayer] playerController(state: .readyToPlay)")
            onBuffering?(["buffering": false])

            if let player = playerView,
               let playerLayer = player.playerLayer {
                let rawDuration = playerLayer.player.duration
                // For DV P5 HLS remux, AVPlayer reports NaN duration for EVENT playlists.
                // Use 0 as fallback — JS side has the correct duration from backend metadata.
                let duration = rawDuration.isFinite && rawDuration > 0 ? rawDuration : 0.0
                let size = playerLayer.player.naturalSize
                print("[KSPlayer] readyToPlay - duration=\(duration) (raw=\(rawDuration)), size=\(size)")

                // Store video source height for bitmap subtitle proportional scaling
                videoSourceHeight = size.height
                updateBitmapSubtitleScale()

                onLoad?([
                    "duration": duration,
                    "width": Int(size.width),
                    "height": Int(size.height)
                ])

                // Observe PiP status changes and sync isPipActive on options
                pipCancellable = playerLayer.$isPipActive
                    .dropFirst() // skip initial value
                    .receive(on: DispatchQueue.main)
                    .sink { [weak self] isActive in
                        print("[KSPlayer] PiP status changed: \(isActive)")
                        self?.currentOptions?.isPipActive = isActive
                        if !isActive {
                            let userPausedInPip = self?.currentOptions?.pipUserPaused ?? false
                            self?.currentOptions?.pipUserPaused = false
                            if userPausedInPip {
                                // User explicitly paused during PiP — stay paused, sync state
                                print("[KSPlayer] PiP ended, user paused during PiP — staying paused")
                                self?.isPaused = true
                            } else if !(self?.isPaused ?? true) {
                                // Was playing and user didn't pause in PiP — resume after
                                // the system's setPlaying(false) during PiP dismissal.
                                DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) { [weak self] in
                                    guard let self = self, !self.isPaused else { return }
                                    print("[KSPlayer] PiP ended, resuming playback")
                                    self.playerView?.play()
                                }
                            }
                            self?.onPipStatusChanged?(["isActive": false, "paused": userPausedInPip])
                        } else {
                            self?.onPipStatusChanged?(["isActive": true])
                        }
                    }

                // Apply volume/mute state (may have been set before playerLayer was ready)
                playerLayer.player.isMuted = volume == 0
                playerLayer.player.playbackVolume = volume

                // Report available tracks
                reportTracks()

                // Re-report tracks after a delay to pick up embedded subtitles (especially PGS/bitmap).
                // VideoPlayerView delays loading embedded subtitles by ~1 second after readyToPlay
                // because some are stored in the video stream and arrive late.
                DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { [weak self] in
                    guard let self = self else { return }
                    let delayedTracks = self.getAvailableTracks()
                    let delayedSubCount = (delayedTracks["subtitleTracks"] as? [[String: Any]])?.count ?? 0
                    self.debugLog("delayed track re-report: subtitle count=\(delayedSubCount)")
                    if delayedSubCount > 0 {
                        self.onTracksChanged?(delayedTracks)
                    }
                }

                // Report video info including HDR and frame rate
                reportVideoInfo(player: playerLayer.player)

                // Handle pending seek
                if let seekTime = pendingSeek {
                    pendingSeek = nil
                    seek(to: seekTime)
                }

                // Apply any pending track selections
                applyPendingTrackSelections()

                // Ensure playback starts if not paused
                // This fixes an issue where play() called before readyToPlay doesn't take effect
                // Use a small delay to ensure React Native has fully applied all props (including paused=false)
                // Without this delay, the paused prop may still be at its default value (true) when we check
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) { [weak self] in
                    guard let self = self else { return }
                    if !self.isPaused {
                        print("[KSPlayer] readyToPlay (delayed): calling play() to ensure playback starts")
                        self.playerView?.play()
                        self.startProgressTimer()
                    } else {
                        print("[KSPlayer] readyToPlay (delayed): isPaused=true, not starting playback")
                    }
                }
            }

        case .buffering:
            print("[KSPlayer] playerController(state: .buffering)")
            onBuffering?(["buffering": true])

        case .bufferFinished:
            print("[KSPlayer] playerController(state: .bufferFinished)")
            onBuffering?(["buffering": false])

        case .playedToTheEnd:
            stopProgressTimer()
            onEnd?(["ended": true])

        case .error:
            stopProgressTimer()
            // Try to get more details about the error from the player
            let errorMessage = "Playback error (state=.error)"
            if let playerLayer = playerView?.playerLayer,
               let player = playerLayer.player as? KSMEPlayer {
                // Log player state for debugging
                print("[KSPlayer] Error state - player.isReadyToPlay: \(player.isReadyToPlay)")
                print("[KSPlayer] Error state - player.duration: \(player.duration)")
                print("[KSPlayer] Error state - player.currentPlaybackTime: \(player.currentPlaybackTime)")
            }
            print("[KSPlayer] playerController(state: .error) - \(errorMessage)")
            onError?(["error": errorMessage])

        default:
            break
        }
    }

    public func playerController(currentTime: TimeInterval, totalTime: TimeInterval) {
        // Progress is reported via timer for consistency
    }

    public func playerController(finish error: Error?) {
        stopProgressTimer()
        if let error = error {
            let errorDesc = error.localizedDescription
            let nsError = error as NSError
            print("[KSPlayer] playerController(finish) error: \(errorDesc)")
            print("[KSPlayer] playerController(finish) NSError domain: \(nsError.domain), code: \(nsError.code)")
            print("[KSPlayer] playerController(finish) userInfo: \(nsError.userInfo)")
            onError?(["error": "\(errorDesc) (code: \(nsError.code))"])
        } else {
            onEnd?(["ended": true])
        }
    }

    public func playerController(maskShow: Bool) {
        // We don't use the built-in UI
    }

    public func playerController(action: PlayerButtonType) {
        // We don't use the built-in UI
    }

    public func playerController(bufferedCount: Int, consumeTime: TimeInterval) {
        // Optional: could report buffer status
    }

    public func playerController(seek: TimeInterval) {
        // User sought via player UI (we don't use this)
    }
}

// MARK: - UIColor Hex Extension

extension UIColor {
    convenience init?(hexString: String) {
        var hex = hexString.trimmingCharacters(in: .whitespacesAndNewlines)
        hex = hex.replacingOccurrences(of: "#", with: "")

        var rgb: UInt64 = 0
        var alpha: CGFloat = 1.0

        guard Scanner(string: hex).scanHexInt64(&rgb) else {
            return nil
        }

        switch hex.count {
        case 6: // RGB (e.g., "FFFFFF")
            let r = CGFloat((rgb >> 16) & 0xFF) / 255.0
            let g = CGFloat((rgb >> 8) & 0xFF) / 255.0
            let b = CGFloat(rgb & 0xFF) / 255.0
            self.init(red: r, green: g, blue: b, alpha: 1.0)

        case 8: // RGBA (e.g., "FFFFFF80" or "00000080")
            let r = CGFloat((rgb >> 24) & 0xFF) / 255.0
            let g = CGFloat((rgb >> 16) & 0xFF) / 255.0
            let b = CGFloat((rgb >> 8) & 0xFF) / 255.0
            alpha = CGFloat(rgb & 0xFF) / 255.0
            self.init(red: r, green: g, blue: b, alpha: alpha)

        default:
            return nil
        }
    }
}
