cmake_minimum_required(VERSION 3.13)
project(runner LANGUAGES CXX)

# Define the application target. To change its name, change BINARY_NAME in the
# top-level CMakeLists.txt, not the value here, or `flutter run` will no longer
# work.
#
# Any new source files that you add to the application should be added here.
add_executable(${BINARY_NAME}
  "main.cc"
  "my_application.cc"
  "desktop_host.cc"
  "${FLUTTER_MANAGED_DIR}/generated_plugin_registrant.cc"
)

# Apply the standard set of build settings. This can be removed for applications
# that need different build settings.
apply_standard_settings(${BINARY_NAME})

# Add preprocessor definitions for the application ID.
add_definitions(-DAPPLICATION_ID="${APPLICATION_ID}")

# Add dependency libraries. Add any application-specific dependencies here.
target_link_libraries(${BINARY_NAME} PRIVATE flutter)
target_link_libraries(${BINARY_NAME} PRIVATE PkgConfig::GTK)
target_link_libraries(${BINARY_NAME} PRIVATE PkgConfig::INDICATOR PkgConfig::GMODULE PkgConfig::EPOXY)

# The camera, the only GStreamer user, is a module the runner opens at its first use, so a
# system without GStreamer still starts the app.
add_library(elarni_camera MODULE "elarni_camera.cc")
apply_standard_settings(elarni_camera)
target_link_libraries(elarni_camera PRIVATE PkgConfig::GSTREAMER)
add_dependencies(${BINARY_NAME} elarni_camera)
# Both name the app build, as the runner opens only its own build's module: an in-place upgrade
# replaces the module under a runner that is still running.
target_compile_definitions(${BINARY_NAME} PRIVATE ELARNI_BUILD="${FLUTTER_VERSION}")
target_compile_definitions(elarni_camera PRIVATE ELARNI_BUILD="${FLUTTER_VERSION}")

target_include_directories(${BINARY_NAME} PRIVATE "${CMAKE_SOURCE_DIR}")
