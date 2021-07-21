Proof-of-Concept project to test various approaches for cleaning up cluster-scoped resources on operator uninstall. 


Option 1:
- auto-create a cluster-scoped CR on operator start 
- look up a ClusterRole that belongs to the operator and is owned by a CSV
- add the ClusterRole as the owner of the CR via ownerReference
- uninstall the operator & the resource is garbage-collected
