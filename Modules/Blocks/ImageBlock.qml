import QtQuick
import qs.Common
import qs.DCommon.Common
import qs.DCommon.Widgets

// One image from a reply, drawn as a card rather than inside the prose run.
//
// Qt's rich text does draw `<img>` — measured, including over http — but only at its natural size: a
// 300 px image inside a 200 px field overflows (contentWidth 300 against a 200 px edit), and a `width`
// attribute scales a small image *up* to whatever it says. A widget can cap both dimensions, keep the
// aspect ratio, show something while it loads, and fall back to the address when it fails.
//
// Only an image alone in its paragraph becomes one of these. An image inside a sentence stays in the
// prose run as its `[image: alt]` link, because splitting a paragraph around a widget would break the
// selection Qt gives us across the whole run.
Item {
    id: root

    property var blk: ({})

    readonly property string url: (blk && blk.url) ? String(blk.url) : ""
    readonly property string alt: (blk && blk.alt) ? String(blk.alt) : ""
    // The alt text, or the tooltip title when there is no alt: whichever names the picture.
    readonly property string label: alt !== "" ? alt : ((blk && blk.title) ? String(blk.title) : "")

    // A screenshot of a terminal is tall and mostly text; unbounded it would push the rest of the reply
    // thousands of pixels down. The image is shrunk to fit either cap and never enlarged.
    readonly property int maxWidth: Math.max(160, width - card.pad * 2)
    readonly property int maxHeight: Math.round(Theme.fontSizeMedium * 26)

    readonly property bool failed: url !== "" && image.status === Image.Error
    readonly property bool ready: image.status === Image.Ready

    // DCard anchors its content rather than sizing to it (it is a surface, not a layout), so the height
    // is computed here — the same shape CodeBlock uses.
    implicitWidth: card.width
    implicitHeight: card.height

    DCard {
        id: card

        width: root.width
        height: column.implicitHeight + pad * 2
        visible: root.url !== ""
        pad: Theme.spacingXS
        radius: Theme.cornerRadiusM
        color: Theme.surfaceContainerHigh
        border.width: Theme.outlineWidth
        border.color: Theme.outlineVariant

        Column {
            id: column

            width: parent.width
            spacing: Theme.spacingXS

            // Shown until the picture is there, and kept after a failure: an image that silently fails
            // to load is a hole where the model said there was a picture, and the address is the only
            // way to find out what it meant.
            StyledText {
                width: parent.width
                visible: !root.ready
                text: root.label !== ""
                    ? I18n.tr("Image: %1", "placeholder while an image loads or after it failed, %1 is the alt text").arg(root.label)
                    : I18n.tr("Image", "placeholder while an image loads or after it failed")
                font.pixelSize: Theme.fontSizeSmall
                color: root.failed ? Theme.error : Theme.surfaceVariantText
                wrapMode: Text.WordWrap
            }

            StyledText {
                width: parent.width
                visible: root.failed
                text: root.url
                font.pixelSize: Theme.fontSizeSmall
                color: Theme.primary
                wrapMode: Text.WrapAnywhere
                HoverHandler {
                    cursorShape: Qt.PointingHandCursor
                }
                TapHandler {
                    onTapped: Qt.openUrlExternally(root.url)
                }
            }

            // Remote URLs are fetched by Qt Quick's own image loader, so opening a reply that mentions
            // one makes a request to whoever hosts it. That is worth knowing; it is also how every other
            // Markdown viewer behaves, and this app is a coding agent whose model can already fetch URLs
            // through its tools.
            Image {
                id: image

                // No `sourceSize`: with it set, Qt reports the *fitted box* as the implicit size and
                // scales the picture up to fill it — measured, a 100x50 image in a 552x364 box came back
                // as 728x364, forty times its size. Without it, implicitWidth/implicitHeight are the
                // image's own size, and the scale below is the only thing that resizes it — down, never
                // up. The cost is decoding at the file's size, which for a chat transcript is fine.
                readonly property real fitScale: {
                    if (image.implicitWidth <= 0 || image.implicitHeight <= 0)
                        return 0;
                    return Math.min(1, root.maxWidth / image.implicitWidth, root.maxHeight / image.implicitHeight);
                }

                width: fitScale > 0 ? Math.round(image.implicitWidth * fitScale) : 0
                height: fitScale > 0 ? Math.round(image.implicitHeight * fitScale) : 0
                visible: root.ready && width > 0
                source: root.url
                asynchronous: true
                fillMode: Image.PreserveAspectFit
                smooth: true

                HoverHandler {
                    cursorShape: Qt.PointingHandCursor
                }
                TapHandler {
                    // The full picture, in whatever the desktop opens images with.
                    onTapped: Qt.openUrlExternally(root.url)
                }
            }
        }
    }
}
