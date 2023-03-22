# k8s-stsupgrader
A controller that enables rolling upgrades in statefulsets (sts). By default sts have a built-in controller that is
responsible for their upgrades. However, the folllowing settings in a sts allows stsupgrader manage their upgrade


1. The following annotation needs to be set in the sts
   
   `stsupdater.salesforce.com/managed: true`
   
2. The statefulset `updateStrategy` needs to be set to `OnDelete`
   ([see updateStrategy](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#on-delete)).
   
3. stsupgrader will update one pod at a time. To update a batch of pods, you can specify the batch size (to say 5)
    by setting the following annotation on the sts:
    
    `stsupdater.salesforce.com/batch: 5`
    
4. By default stsupgrader treats all Pods of the sts as equal. However, if one of the pods is a master or active
   instance of some service (like ZooKeeper leader) and needs to be upgraded last, then that Pod should label
   itself as the master by setting the following lable and value
   
   `stsupdater.salesforce.com/masterpod: true`

5. Enable Lease based upgrades: The following annotations must be included for any statefulset that you want it enabled for

   `stsupdater.salesforce.com/leasename: nodepool-[insert nodepool name here]`
   `stsupdater.salesforce.com/leasenamespace: sfdc-system`

   Must also ensure that the option "enableLease" is set to true when deploying stsupgrader 

### Build
`go build  -mod vendor`
 
### Unit Test
 `go test -v ./...  -mod vendor`

### Docker Build
For EKS, you can build docker container as follows

### Vendor Packages
If you add new packages as dependencies, run

`go mod vendor`

and then commit the changes into git.



