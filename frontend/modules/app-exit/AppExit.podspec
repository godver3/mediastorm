require 'json'

package = JSON.parse(File.read(File.join(__dir__, 'package.json')))

Pod::Spec.new do |s|
  s.name           = 'AppExit'
  s.version        = package['version']
  s.summary        = 'Exit app module for TV platforms'
  s.description    = 'Expo module for programmatically exiting the app on TV platforms'
  s.author         = package['author']
  s.license        = package['license']
  s.homepage       = 'https://github.com/example/app-exit'
  s.platforms      = { :ios => '15.1', :tvos => '15.1' }
  s.source         = { :git => 'https://github.com/example/app-exit.git', :tag => "v#{s.version}" }
  s.static_framework = true

  s.dependency 'ExpoModulesCore'

  s.source_files = 'ios/**/*.swift'
  s.swift_version = '5.4'
end
