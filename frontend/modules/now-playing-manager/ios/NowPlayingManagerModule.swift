import ExpoModulesCore
import MediaPlayer

struct NowPlayingInfoRecord: Record {
  @Field var title: String?
  @Field var subtitle: String?
  @Field var duration: Double?
  @Field var currentTime: Double?
  @Field var playbackRate: Double?
  @Field var imageUri: String?
}

public class NowPlayingManagerModule: Module {
  private var cachedArtworkURL: String?
  private var cachedArtworkImage: UIImage?

  public func definition() -> ModuleDefinition {
    Name("NowPlayingManager")

    AsyncFunction("updateNowPlaying") { (info: NowPlayingInfoRecord) in
      var nowPlayingInfo = MPNowPlayingInfoCenter.default().nowPlayingInfo ?? [String: Any]()

      if let title = info.title {
        nowPlayingInfo[MPMediaItemPropertyTitle] = title
      }
      if let subtitle = info.subtitle {
        nowPlayingInfo[MPMediaItemPropertyArtist] = subtitle
      }
      if let duration = info.duration {
        nowPlayingInfo[MPMediaItemPropertyPlaybackDuration] = duration
      }
      if let currentTime = info.currentTime {
        nowPlayingInfo[MPNowPlayingInfoPropertyElapsedPlaybackTime] = currentTime
      }
      if let playbackRate = info.playbackRate {
        nowPlayingInfo[MPNowPlayingInfoPropertyDefaultPlaybackRate] = 1.0
        nowPlayingInfo[MPNowPlayingInfoPropertyPlaybackRate] = playbackRate
      }

      MPNowPlayingInfoCenter.default().nowPlayingInfo = nowPlayingInfo

      // Download and set artwork asynchronously
      if let imageUri = info.imageUri, !imageUri.isEmpty {
        self.setArtwork(from: imageUri)
      }
    }

    AsyncFunction("updatePlaybackPosition") { (currentTime: Double, duration: Double, playbackRate: Double) in
      guard var nowPlayingInfo = MPNowPlayingInfoCenter.default().nowPlayingInfo else { return }
      nowPlayingInfo[MPNowPlayingInfoPropertyElapsedPlaybackTime] = currentTime
      nowPlayingInfo[MPMediaItemPropertyPlaybackDuration] = duration
      nowPlayingInfo[MPNowPlayingInfoPropertyPlaybackRate] = playbackRate
      MPNowPlayingInfoCenter.default().nowPlayingInfo = nowPlayingInfo
    }

    AsyncFunction("clearNowPlaying") {
      MPNowPlayingInfoCenter.default().nowPlayingInfo = nil
      self.cachedArtworkURL = nil
      self.cachedArtworkImage = nil
    }

    AsyncFunction("setupRemoteCommands") {
      // Remote commands are handled by KSPlayer's registerRemoteControllEvent().
      // This function exists to satisfy the JS interface but is intentionally a no-op
      // to avoid duplicate MPRemoteCommandCenter handlers that would cause conflicts
      // (e.g. double play/pause on iOS, Siri Remote issues on tvOS).
    }
  }

  private func setArtwork(from urlString: String) {
    // Skip if we already have this artwork cached
    if urlString == cachedArtworkURL, let cachedImage = cachedArtworkImage {
      applyArtwork(cachedImage)
      return
    }

    guard let url = URL(string: urlString) else { return }

    URLSession.shared.dataTask(with: url) { [weak self] data, _, error in
      guard let self = self, error == nil, let data = data, let image = UIImage(data: data) else {
        return
      }

      self.cachedArtworkURL = urlString
      self.cachedArtworkImage = image
      self.applyArtwork(image)
    }.resume()
  }

  private func applyArtwork(_ image: UIImage) {
    guard var nowPlayingInfo = MPNowPlayingInfoCenter.default().nowPlayingInfo else { return }

    let artwork = MPMediaItemArtwork(boundsSize: image.size) { _ in
      return image
    }
    nowPlayingInfo[MPMediaItemPropertyArtwork] = artwork
    MPNowPlayingInfoCenter.default().nowPlayingInfo = nowPlayingInfo
  }
}
