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
    private var pendingSeek: Double?
    private var pendingAudioTrack: Int?
    private var pendingSubtitleTrack: Int?
    private var subtitleRetryCount: Int = 0
    private var lastReportedDynamicRange: String = ""

    // MARK: - React Native Event Blocks
    @objc var onLoad: RCTDirectEventBlock?
    @objc var onProgress: RCTDirectEventBlock?
    @objc var onEnd: RCTDirectEventBlock?
    @objc var onError: RCTDirectEventBlock?
    @objc var onTracksChanged: RCTDirectEventBlock?
    @objc var onBuffering: RCTDirectEventBlock?
    @objc var onVideoInfo: RCTDirectEventBlock?
    @objc var onDebugLog: RCTDirectEventBlock?

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

        print("[KSPlayer] Global settings: firstPlayer=KSMEPlayer, hwDecode=\(KSOptions.hardwareDecode), asyncDecomp=\(KSOptions.asynchronousDecompression)")

        // Create the player view
        let player = VideoPlayerView()
        player.translatesAutoresizingMaskIntoConstraints = false
        player.delegate = self

        // Hide native controls - we use our own UI
        player.toolBar.isHidden = true
        player.toolBar.timeSlider.isHidden = true
        player.navigationBar.isHidden = true
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

        // Only send valid numbers
        guard currentTime.isFinite && duration.isFinite else { return }

        onProgress?([
            "currentTime": currentTime,
            "duration": duration
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

        debugLog("setSource: uri=\(uri.prefix(100))...")
        debugLog("setSource: headers=\(source["headers"] ?? "nil")")

        // Reset state for new source
        currentSource = source
        lastReportedDynamicRange = ""
        subtitleRetryTimer?.invalidate()
        subtitleRetryTimer = nil
        subtitleRetryCount = 0

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
        playerView?.playerLayer?.player.isMuted = volume == 0
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
    }

    func applySubtitleStyle(_ style: NSDictionary?) {
        guard let style = style else { return }

        // Font size (multiplier, 1.0 = default)
        if let fontSize = style["fontSize"] as? Double {
            // KSPlayer's default font size is around 20pt on iOS
            // Scale it by the multiplier
            // tvOS needs larger base size due to viewing distance (matching SubtitleOverlay at 62pt)
            #if os(tvOS)
            let baseSize: CGFloat = 60.0
            #else
            let baseSize: CGFloat = 24.0
            #endif
            SubtitleModel.textFontSize = baseSize * CGFloat(fontSize)
            // Update the label's font to reflect the new size
            // (SubtitleModel.textFont is a computed property that uses textFontSize)
            playerView?.subtitleLabel.font = SubtitleModel.textFont
            print("[KSPlayer] Subtitle font size set to: \(SubtitleModel.textFontSize)")
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

        let controlsOffset: CGFloat = controlsVisible ? -100 : 0

        DispatchQueue.main.async {
            // Ensure parent views don't clip the subtitles when they move up
            // KSPlayer's internal views may have clipsToBounds enabled by default
            pv.clipsToBounds = false
            pv.subtitleBackView.superview?.clipsToBounds = false
            pv.subtitleLabel.superview?.clipsToBounds = false

            UIView.animate(withDuration: 0.2) {
                pv.subtitleBackView.transform = CGAffineTransform(translationX: 0, y: controlsOffset)
                pv.subtitleLabel.transform = CGAffineTransform(translationX: 0, y: controlsOffset)
            }
        }
        print("[KSPlayer] Subtitle position updated: controlsVisible=\(controlsVisible), offset=\(controlsOffset)")
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
            return ["audioTracks": [], "subtitleTracks": []]
        }

        let audioTracks = player.tracks(mediaType: .audio).enumerated().map { (index, track) -> [String: Any] in
            return [
                "id": index,
                "type": "audio",
                "title": track.name,
                "language": track.language ?? "",
                "codec": String(track.codecType),
                "selected": track.isEnabled
            ]
        }

        let subtitleTracks = player.tracks(mediaType: .subtitle).enumerated().map { (index, track) -> [String: Any] in
            return [
                "id": index,
                "type": "subtitle",
                "title": track.name,
                "language": track.language ?? "",
                "codec": String(track.codecType),
                "selected": track.isEnabled
            ]
        }

        return [
            "audioTracks": audioTracks,
            "subtitleTracks": subtitleTracks
        ]
    }

    private func reportTracks() {
        let tracks = getAvailableTracks()
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
                let duration = playerLayer.player.duration
                let size = playerLayer.player.naturalSize
                print("[KSPlayer] readyToPlay - duration=\(duration), size=\(size)")

                onLoad?([
                    "duration": duration,
                    "width": Int(size.width),
                    "height": Int(size.height)
                ])

                // Report available tracks
                reportTracks()

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
