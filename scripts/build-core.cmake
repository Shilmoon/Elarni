# Builds elarni-core for an app bundle, with the digest of the ShadowQUIC client that ships
# next to it: the core's elevated helper copies and starts only that program.
#
#   cmake -DGO=<go> -DROOT=<repository> -DCLIENT=<elarni-shadowquic> -DOUTPUT=<elarni-core>
#         [-DTAGS=-tags=<tags>] [-DGOOS=<os> -DGOARCH=<arch>] -P scripts/build-core.cmake
foreach(name GO ROOT CLIENT OUTPUT)
  if(NOT ${name})
    message(FATAL_ERROR "build-core.cmake needs -D${name}=")
  endif()
endforeach()
file(SHA256 "${CLIENT}" digest)
set(environment CGO_ENABLED=0)
if(GOOS)
  list(APPEND environment "GOOS=${GOOS}" "GOARCH=${GOARCH}")
endif()
set(arguments build -trimpath)
if(TAGS)
  list(APPEND arguments "${TAGS}")
endif()
execute_process(
  COMMAND "${CMAKE_COMMAND}" -E env ${environment} "${GO}" ${arguments}
    -ldflags "-X main.shadowquicSHA256=${digest}" -o "${OUTPUT}" ./Vendor/libXray/cmd/elarni-core
  WORKING_DIRECTORY "${ROOT}" RESULT_VARIABLE result)
if(NOT result EQUAL 0)
  message(FATAL_ERROR "elarni-core did not build.")
endif()
