require 'json'

package = JSON.parse(File.read(File.join(__dir__, 'package.json')))

Pod::Spec.new do |s|
  s.name           = 'KSPlayerModule'
  s.version        = package['version']
  s.summary        = 'React Native wrapper for KSPlayer native video player'
  s.description    = 'React Native module wrapping KSPlayer for native video playback with FFmpeg support on iOS/tvOS'
  s.author         = package['author']
  s.license        = package['license']
  s.homepage       = 'https://github.com/example/ksplayer-rn'
  s.platforms      = { :ios => '15.1', :tvos => '17.0' }
  s.source         = { :git => 'https://github.com/example/ksplayer-rn.git', :tag => "v#{s.version}" }
  s.static_framework = true

  s.dependency 'React-Core'
  s.dependency 'KSPlayer'

  s.source_files = 'ios/**/*.{h,m,swift}'
  s.swift_version = '5.9'
end
