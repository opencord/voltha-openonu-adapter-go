#Notes on OMCI Alarms Supported by VOLTHA
Below Table describes the list of OMCI Alarm notifications supported by VOLTHA:

| ME Class ID  | Alarm BitMap No | Alarm Name | Description |
| ------------ | --------------- | ---------- | ----------- |
| CircuitPackClassID | 0 | Equipment Alarm | A failure on an internal interface or failed self-test |
| CircuitPackClassID | 2 | ONU self-test failure | Failure of circuit pack autonomous self-test |
| CircuitPackClassID | 3 | Laser end of life | Failure of transmit laser imminent |
| CircuitPackClassID | 4 |Temperature yellow | No service shutdown at present, but the circuit pack is operating beyond its recommended range |
| CircuitPackClassID | 5 |Temperature red | Service has been shut down to avoid equipment damage. The operational state of the affected PPTPs indicates the affected services |
| PhysicalPathTerminationPointEthernetUniClassID | 0 | LAN-LOS | No carrier at the Ethernet UNI |
| OnuGClassID | 0 | Equipment Alarm | Functional failure on an internal interface |
| OnuGClassID | 6 | ONU self-test failure | ONU has failed autonomous self-test |
| OnuGClassID | 7 | Dying gasp | ONU is powering off imminently due to loss of power to the ONU itself |
| OnuGClassID | 8 | Temperature yellow | No service shutdown at present, but the circuit pack is operating beyond its recommended range |
| OnuGClassID | 9 | Temperature red | Some services have been shut down to avoid equipment damage. The operational state of the affected PPTPs indicates the affected services |
| OnuGClassID | 10 | Voltage yellow | No service shutdown at present, but the line power voltage is below its recommended minimum. Service restrictions may be in effect, such as permitting no more than N lines off-hook or ringing at one time |
| OnuGClassID | 11 | Voltage red | Some services have been shut down to avoid power collapse. The operational state of the affected PPTPs indicates the affected services |
| AniGClassID | 0 | Low received optical power | Received downstream optical power below threshold |
| AniGClassID | 1 | High received optical power | Received downstream optical power above threshold |
| AniGClassID | 4 | Low transmit optical power | Transmit optical power below lower threshold |
| AniGClassID | 5 | High transmit optical power | Transmit optical power above upper threshold |
| AniGClassID | 6 | Laser bias current | Laser bias current above threshold determined by vendor; laser end of life pending |
