require 'json'

package = JSON.parse(File.read(File.join(__dir__, 'package.json')))

Pod::Spec.new do |s|
  s.name           = 'NowPlayingManager'
  s.version        = package['version']
  s.summary        = 'Now Playing info manager for iOS Control Center'
  s.description    = 'Expo module for managing MPNowPlayingInfoCenter on iOS'
  s.author         = package['author']
  s.license        = package['license']
  s.homepage       = 'https://github.com/example/now-playing-manager'
  s.platforms      = { :ios => '15.1', :tvos => '15.1' }
  s.source         = { :git => 'https://github.com/example/now-playing-manager.git', :tag => "v#{s.version}" }
  s.static_framework = true

  s.dependency 'ExpoModulesCore'

  s.source_files = 'ios/**/*.swift'
  s.swift_version = '5.4'
end
