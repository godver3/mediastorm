import ExpoModulesCore

public class AppExitModule: Module {
  public func definition() -> ModuleDefinition {
    Name("AppExit")

    Function("exitApp") {
      DispatchQueue.main.async {
        exit(0)
      }
    }
  }
}
