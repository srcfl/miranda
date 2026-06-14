# vhs tape for the Miranda demo GIF.  Render with:  vhs assets/demo.tape
# Produces assets/miranda-demo.gif from the illustrative assets/demo.sh.

Output assets/miranda-demo.gif

Set Shell "bash"
Set FontSize 20
Set Width 1200
Set Height 680
Set Padding 26
Set Theme "Dracula"

Hide
Type "clear"
Enter
Type "bash assets/demo.sh"
Enter
Sleep 600ms
Show
Sleep 12s
