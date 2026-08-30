# vhs tape for the Miranda demo GIF.  Render from the repo root with:
#
#   vhs assets/demo.tape
#
# Produces assets/miranda-demo.gif from the illustrative assets/demo.sh.
# The canvas is sized so `mir share`'s invite QR fits on one screen without
# scrolling: 43 rows x 102 columns at this font size and padding.

Output assets/miranda-demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1100
Set Height 880
Set Padding 26
Set Theme "Dracula"

Hide
Type "clear"
Enter
Type "bash assets/demo.sh"
Enter
Sleep 600ms
Show
Sleep 13800ms
