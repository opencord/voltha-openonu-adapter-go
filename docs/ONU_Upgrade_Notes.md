# Notes on ONU upgrade procedures using the openonu-go adapter
Openonu-go adapter supports the download and activation of new ONU software from some external http(s) server to the ONUs over the OMCI channel.
These are the relevant voltctl (image) commands for the ONU upgrade procedure:
- voltctl device onuimage [list](#images-list-on-the-onu)
- voltctl device onuimage [status](#image-status-display)
- voltctl device onuimage [download](#image-download-from-http-server-to-the-onu)
- voltctl device onuimage [activate](#image-activation-on-the-onu)
- voltctl device onuimage [commit](#image-commit-on-the-onu)
- voltctl device onuimage [abort](#image-activity-abort)


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

## Images list on the Onu

The syntax of the complete voltctl command is:
```bash
voltctl device onuimage list <onu-device-id>
```
This command displays OMIC information on the active and standby images on the device.

Here is some example output for this command:
```bash
VERSION           ISCOMMITED    ISACTIVE    ISVALID    PRODUCTCODE    HASH
090140.1.0.304    true          true        true                      00000000000000000000000000000000
090140.9.9.308    false         false       false                     00000000000000000000000000000000
```
The command output lists:
- the `VERSION` the version of each image
- the `ISCOMMITED` is the image commited on the ONU
- the `ISACTIVE` is the image active on the ONU
- the `ISVALID` is the image valide for that ONU
- the `PRODUCTCODE` infomration about the image that can be provided by the ONU via OMCI. (optional)
- the `HASH` infomration about the image that can be provided by the ONU via OMCI. (optional)

All the attributes specified in the output of this command are retrieved from the ONU via OMCI as per G.988 specification.

## Image status display

The syntax of the complete voltctl command is:
```
voltctl device onuimage status <image-version> <onu-device-ids>
```
with:
- `<image-version>`: version of the image, e.g `090140.1.0.304`.
- `<onu-device-ids>`: set of devices for which to display the status of the given image.

Here is some example output for this command:
```bash
DEVICEID                                IMAGESTATE.VERSION    IMAGESTATE.DOWNLOADSTATE    IMAGESTATE.REASON    IMAGESTATE.IMAGESTATE
420792c1-84fb-465a-a775-283e54247be9    090140.1.0.304        DOWNLOAD_SUCCEEDED          NO_ERROR             IMAGE_INACTIVE
```

The command output lists:
- the `DOWNLOADSTATE` with values defined in the protobuf implementation for the [image download](https://github.com/opencord/voltha-protos/blob/v4.0.11/protos/voltha_protos/device.proto#L106)
- the `IMAGESTATE` with values defined in the protobuf implementation for the [image activation](https://github.com/opencord/voltha-protos/blob/v4.0.11/protos/voltha_protos/device.proto#L125)
- the `REASON` with values defined in the protobuf implementation for the [image failure reasons](https://github.com/opencord/voltha-protos/blob/v4.0.11/protos/voltha_protos/device.proto#L116)

This command exposes the status of operations in relation to an image on one or multiple devices.

In above example output the given states indicate that for image software-image.img the download first to the openonu-go adapter
and then to the ONU was correctly done. The image is in the inactive ONU partition (not yet activated).

## Image download from http server to the Onu

The syntax of the complete voltctl command is:
```
voltctl device onuimage download <image-version> <image-url> <image-vendor> <activate-on-success> <commit-on-success> <crc> <onu-device-ids>
```
with:
- `<image-version>`: version of the image, e.g `090140.1.0.304`.
- `<image-url>`: A full path of the location of the image in a given location, e.g. `http://10.103.21.52:8080/downloads/090140.1.0.304.img `.
- `<image-vendor>`: the ONU vendor for which the image can be used, e.g. `SCOM`.
- `<activate-on-success>`: activate the image automatically if the download is successful.
- `<commit-on-success>`: commits the image automatically on succesful reboot of the ONU after activation.
- `<crc>`: the crc of the image as described in OMCI specification. Can be set to `0`, in which case the onu adapter will compute it.
- `<onu-device-ids>`: set of devices on which to activate the given image.

A sample output of this command is:

```bash
DEVICEID                                IMAGESTATE.VERSION    IMAGESTATE.DOWNLOADSTATE    IMAGESTATE.REASON    IMAGESTATE.IMAGESTATE
420792c1-84fb-465a-a775-283e54247be9    090140.1.0.304        DOWNLOAD_STARTED            NO_ERROR             IMAGE_DOWNLOADING
```

This command downloads the given image on one or multiple devices. First it fetches the image from and http(s) server
if the image is not already present in the openonu adapter. It then transfers the image to the device via OMCI,
placing it in the `standby` partition.

The processing of the command can basically be verified with above given image [status](#image-status-display) command,
where the `DOWNLOADSTATE` defines the progress of this activity.
Depending on the file size and the IP connectivity the transfer may take some seconds up to minutes.
Image status directly on the ONU device can always be performed by the [list](#images-list-on-the-onu) command.

The download fails e.g. in case the image cannot be found on the server, in which case the `DOWNLOADSTATE` goes to "DOWNLOAD_FAILED"

If the `ActivateOnSuccess` flag is set the adapter will automatically activate the image upon successful download to the ONU.
If the `CommitOnSuccess` flag is set the adapter will automatically commit the image upon successful reboot of the ONU.


## Image activation on the onu

The syntax of the complete voltctl command is:
```
voltctl device onuimage activate <image-version> <commit-on-success> <onu-device-ids>
```
with:
- `<image-version>`: version of the image, the same identifier as used in the [download](#image-download-from-http-server-to-the-onu) or the real inactive image version of the ONU (as given in the [list](#images-list-on-the-onu) command)
- `<commit-on-success>`: commits the image automatically if activation is successful.
- `<onu-device-ids>`: set of devices on which to activate the given image.

A sample output of this command is:

```bash
DEVICEID                                IMAGESTATE.VERSION    IMAGESTATE.DOWNLOADSTATE    IMAGESTATE.REASON    IMAGESTATE.IMAGESTATE
420792c1-84fb-465a-a775-283e54247be9    090140.1.0.304        DOWNLOAD_SUCCEEDED          NO_ERROR             IMAGE_ACTIVATING
 ```

This command activates the given image on one or multiple devices, if the requested image is in the inactive ONU partition.
This implies the switching of the active partition and reboot of the ONU. Effectively moves the image from the `standby` to
the `active` partition. The previously `standby` image will be active after the ONU re-start.
If the image for which the `activate` request arrives is already in the `active` partition no action will be taken.

The processing of the command can be verified by checking the `IMAGESTATE` in the initial output of the command and then
in result of the above given image [status](#image-status-display) command.
Image status directly on the ONU device can always be performed by the [list](#images-list-on-the-onu) command.

If the `CommitOnSuccess` flag is set the adapter will automatically commit the image upon successful reboot of the ONU.


## Image commit on the Onu

The syntax of the complete voltctl command is:
```
voltctl device onuimage commit <image-version> <onu-device-ids>
```
with:
- `<image-version>`: version of the image, the same identifier as used in the [download](#image-download-from-http-server-to-the-onu) or [activate](#image-activation-on-the-onu) command or the real active-uncommitted image version of the ONU (as given in the [list](#images-list-on-the-onu) command)
- `<onu-device-ids>`: set of devices on which to commit the given image.

A sample output of this command is:

```bash
DEVICEID                                IMAGESTATE.VERSION    IMAGESTATE.DOWNLOADSTATE    IMAGESTATE.REASON    IMAGESTATE.IMAGESTATE
420792c1-84fb-465a-a775-283e54247be9    090140.1.0.304        DOWNLOAD_UNKNOWN            NO_ERROR             IMAGE_COMMITTING
```
This command commits the given image on one or multiple devices. The commit command confirms the image on the `active` partition
to be the default one upon reboot.

The processing of the command can be verified by checking the `IMAGESTATE` in the initial output of the command and then
in result of the above given image [status](#image-status-display) command.

The result of this command can be verified using the [list](#images-list-on-the-onu) command which should indicate the requested image with `IsCommitted` and `IsActive` flags set to true.

## Image activity abort

The syntax of the complete voltctl command is:
```
voltctl device onuimage abort <image-version> <onu-device-ids>
```
with:
- `<image-version>`: version of the image, the same identifier as used in the [download](#image-download-from-http-server-to-the-onu), [activate](#image-activation-on-the-onu) or [commit](#image-commit-on-the-onu) commands
- `<onu-device-ids>`: set of devices on which to abort image processing.

A sample output of this command is:

```bash
DEVICEID                                IMAGESTATE.VERSION    IMAGESTATE.DOWNLOADSTATE    IMAGESTATE.REASON    IMAGESTATE.IMAGESTATE
420792c1-84fb-465a-a775-283e54247be9    090140.1.0.304        DOWNLOAD_UNKNOWN            NO_ERROR             IMAGE_ACTIVATION_ABORTING
```

This command aborts any upgrade related activity that the adapter is performing in relation to the given image for the one or multiple devices.
This command does not imply any automated activity by the system to bring the ONU upgrade status to a previous or more consistent state.
If required some further commands can be used to try to bring the ONU into the desired upgrade state.

The processing of the command can be verified with above given image [status](#image-status-display) command,
where the `IMAGESTATE` defines the progress of this activity.
Image status directly on the ONU device can always be performed by the [list](#images-list-on-the-onu) command.

## User interface upgrade progress verification

The 'activity' commands [download](#image-download-from-http-server-to-the-onu), [activate](#image-activation-on-the-onu), [commit](#image-commit-on-the-onu) and [abort](#image-activity-abort) provide an immediate response for the ONU related image states. These states then indicate that either the requested activity has just started or some processing problem was immediately detected that denies the execution of the request. In case no immediate error was found the requested processing is internally (in the ONU adapter) continued. This makes it necessary to verify the progress of the activity using the [status](#image-status-display) command. On success or later failure detection according image state indications are given that can be verified in order to detect the end and the result of the activity.

The PDF document [OnuUpgradeStatesOvw](docs/OnuUpgradeStatesOvw.pdf) gives on overview of the main image state transitions implemented in the ONU adapter and can be used as guideline for such user interface verification.
In the end the [list](#images-list-on-the-onu) command should be used in any case to verify the real situation on the concerned ONU.
