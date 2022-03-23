# MIB Templates

## Important Note:
Unlike for the previous Python version of the openonu-adapter, it is no longer absolutely necessary to store a pre-built version of a MIB template in the kv store
in order to benefit from an accelerated startup of the ONUs, since for the first ONU of a given triplet of VendorID, EquipmentID and ActiveSwVersion
a corresponding MIB template is generated during its startup and is stored in the kv store.
However, there is a small risk that the first ONU that registers with the Voltha POD reports a MIB which is syntactically correct but incorrect
in terms of content during MIB upload. This MIB would than be stored by the openonu-adapter as a MIB template in kv store and used for other ONUs
of this type during their startup.
To eliminate this small risk, a predefined MIB template has to be generated for each ONU type and has to be imported into kv store.
To generate this predefined MIB template an ONU of a certain type can be brought up under controlled laboratory conditions in a Voltha POD
and the generated MIB template can be extracted from the kv store at:

**service/\<voltha_name>/omci_mibs/go_templates/\<VendorID>/\<EquipmentID>/\<ActiveSwVersion>/**

To use this MIB template in other Voltha PODs, it has to be imported again at exactly the same kv store path as it was extracted from.
An example for the BBSIM MIB template on how to do this can be found in the script file
https://github.com/opencord/voltha-openonu-adapter-go/blob/master/templates/load-mib-template.sh.
This should be done before the first ONU of this type is launched.