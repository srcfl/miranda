# vhs tape for the Miranda demo GIF.  Render from the repo root with:
#
#   vhs assets/demo.tape
#
# Produces assets/miranda-demo.gif from the illustrative assets/demo.sh.
# The canvas is sized so `mir share`'s invite QR fits on one screen without
# scrolling: 42 rows x 80 columns at this font size and padding.  The last beat
# needs 41 of those rows, and 80 columns is the narrowest that still wraps each
# invite URL to two lines rather than three — narrow keeps the cells (and the
# 📱 in the mint header) as large as the README's 900px column allows.
#
# Keep Width and Height EVEN.  An odd width defeats the palette pass and the
# same recording lands at 14 MB instead of ~210 KB.

Output assets/miranda-demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 884
Set Height 878
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
