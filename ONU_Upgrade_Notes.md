# Notes on ONU upgrade procedures using the openonu-go adapter
Openonu-go adapter supports the download and activation of new ONU software from some external http(s) server to the ONUs over the OMCI channel.
There are three relevant voltctl (image) commands for the ONU upgrade procedure:
- voltctl device image [list](#image-state-display)
- voltctl device image [download](#image-download-from-http(s)-server-to-the-OnuAdapter)
- voltctl device image [activate](#image-download-from-onuadapter-to-onu-and-activation)

The complete ONU upgrade process consists of:
- downloading the requested ONU Image from some http(s) server that must be reachable from the VOLTHA stack
- transferring this image via the OMCI communication channel to the concerned ONU based on the `software image` activities as defined in the `G.988 specification`
- activating the image on the ONU (which should result into some autonomous ONU reboot)
- committing the new image if it was detected to work correctly

Preconditions:
- HTTP(s)/IP connectivity is given between the VOLTHA stack and the indicated HTTP(s) server
- ONU uses dual image control handling (image ID usage 0 and 1 according to G.988)
- ONU supports software download via the OMCI communication channel as per G.988

The command syntax is based on upgrading one specific ONU of the system. The concerned `<onu-device-id>` can e.g. be found by using this command and taking the 'ID' field from the response:
```
voltctl device list -f Type~brcm_openomci_onu
```


## Image state display

The syntax of the complete voltctl command is:
```
voltctl device image list <onu-device-id>
```

Here is some example output for this command:
```
NAME                  URL                           CRC    DOWNLOADSTATE         IMAGEVERSION    LOCALDIR    IMAGESTATE       FILESIZE
software-image.img    http://bbsim0:50074/images    0      DOWNLOAD_SUCCEEDED                    /tmp        IMAGE_UNKNOWN    0
timg1.img             http://bbsim0:50074/images    0      DOWNLOAD_FAILED                       /tmp        IMAGE_UNKNOWN    0
```

The command output lists:
- the `DOWNLOADSTATE` with values defined in the protobuf implementation for the [image download](https://github.com/opencord/voltha-protos/blob/v4.0.11/protos/voltha_protos/device.proto#L106)
- the `IMAGESTATE` with values defined in the protobuf implementation for the [image activation](https://github.com/opencord/voltha-protos/blob/v4.0.11/protos/voltha_protos/device.proto#L125)

The `IMAGEVERSION` is not displayed here, it is anyway ignored for the download and activation process. The `FILESIZE` is not updated in this release.

In above example output the given states indicate that for image software-image.img the download to the openonu-go adapter was correctly started or even finished but no download to the ONU was yet started, while for image timg1.img some problem for the download to the openonu-go adapter was detected.

## Image download from http(s) server to the OnuAdapter

The syntax of the complete voltctl command is:
```
voltctl device image download <onu-device-id> <image-name> <http-url> <version-string> <crc> <local-path>
```
with:
- `<image-name>`: name of the image as available on the http(s) server, possible format `<identifier>.<extension>`, example for above `list` command was "software-image.img"
- `<http-url>`: http(s) URL of the server path, including the http request indication, example for above `list` command was ```"http://bbsim0:50074/images"```
- `<version-string>`: version indication, ignored in this release, but must be present, example: "v1.0.0 0"
- `<crc>`: not used in this release, should be set to 0
- `<local-path>`: path of the openonu-go adapter to where the downloaded file is to be stored, including linux path slash notation, recommendation is to use "\tmp"

The processing of the command can basically be verified with above given image [list](#image-state-display) command, where the `DOWNLOADSTATE` defines the progress of this activity. Note that in this release the state DOWNLOAD_SUCCEEDED does not really indicate that the file was really downloaded, but the download could be correctly started. Depending on the file size and the IP connectivity the transfer may take some seconds up to minutes.
In this release it is still recommended to check the real success of the download by waiting for or finding some appropriate multiple-element info log level output of the openonu-go adapter like this:
```
"caller":"onuadaptercore/adapter_download_manager.go:210","msg":"written file size is","length":31654,"file":"/tmp/software-image.img"
```

The download fails e.g. in case the image cannot be found on the server, in which case the `DOWNLOADSTATE` goes to "DOWNLOAD_FAILED"


## Image download from OnuAdapter to ONU and activation

The syntax of the complete voltctl command is:
```
voltctl device image activate <onu-device-id> <image-name> <version-string> <crc> <local-path>
```
with:
- `<image-name>`: name of the image, the same identifier as used in the [download](#image-download-from-http(s)-server-to-the-OnuAdapter) command
- `<version-string>`: version indication, the same identifier as used in the [download](#image-download-from-http(s)-server-to-the-OnuAdapter) command, not used in this release
- `<crc>`: not used in this release, should be set to 0 like in the [download](#image-download-from-http(s)-server-to-the-OnuAdapter) command
- `<local-path>`: path of the openonu-go adapter from where the file is downloaded for sending it over the OMCI channel to the ONU, the same identifier as used in the [download](#image-download-from-http(s)-server-to-the-OnuAdapter) command

The processing of the command can basically be verified with above given image [list](#image-state-display) command, where the `IMAGESTATE` defines the progress of this activity. Note that in this release the state IMAGE_ACTIVE does not really indicate that the image was really activated on the ONU, but the transfer over the OMCI channel was correctly started. Depending on the file size and further ONU dependent file handling parameters the transfer may take some minutes.
The correct complete file transfer and start of the activation of the image can be verified by waiting for or finding some appropriate multiple-element info log level output of the openonu-go adapter like this:
```
"caller":"onuadaptercore/omci_onu_upgrade.go:474","msg":"OnuUpgradeFsm activate SW"
```
This shall lead to an autonomous reset of the ONU, which should indicate the previously downloaded image as active after the ONU re-start. If the initial OMCI processing is correctly handled by the ONU, the image is automatically committed by the openonu-go adapter.
In this release it is still recommended to check the real success of the image activation and commitment after ONU reboot by waiting for or finding some appropriate multiple-element info log level output of the openonu-go adapter like this:
```
"caller":"onuadaptercore/omci_onu_upgrade.go:920","msg":"requested SW image committed, releasing OnuUpgrade"
```

## Future work
Some improvements are already planned for the general handling of software upgrade.
This must of course include a simple possibility to verify if the download or activation was really done.
Moreover some extra commands for separate OMCI download and activation are considered to enable time independent processing for these activities.
The commands for removing the images are still missing and unused command parameters may be removed.
