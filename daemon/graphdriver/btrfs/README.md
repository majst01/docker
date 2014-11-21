http://lwn.net/Articles/579009/


btrfs quota enable <path>

Rescan the subvolumes (added in kernel 3.11)
btrfs quota rescan <path>

Then you can assign a limit to any subvolume using;
btrfs qgroup limit 100G <path>/<subvolume>

You can look at quota usage using
btrfs qgroup show <path>
