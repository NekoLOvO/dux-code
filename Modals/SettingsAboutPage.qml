import QtQuick
import qs.Common
import qs.Services
import qs.Widgets
import qs.DCommon.Widgets

Item {
    id: root

    LayoutMirroring.enabled: I18n.isRtl
    LayoutMirroring.childrenInherit: true

    readonly property string githubUrl: "https://github.com/NekoLOvO/dux-code"
    // TODO: fill in once there is a docs site.
    readonly property string docsUrl: ""
    readonly property string licenseUrl: githubUrl + "/blob/main/LICENSE"
    readonly property string version: Backend.daemonVersion.match(/^v?(\d+\.\d+)/)?.[1] ?? ""

    function host(url) {
        return url.replace(/^https?:\/\//, "").replace(/\/$/, "");
    }

    SettingsPage {
        DHeroCard {
            brand: I18n.tr("Code", "about page hero, app name")
            caption: root.version ? `dcode ${root.version}` : "dcode"
        }

        SettingsCard {
            SettingsRow {
                body: StyledText {
                    text: I18n.tr('Dux Code is a native coding agent for the <a href="%2" style="text-decoration:none; color:%1;">Dux Shell</a> desktop. The model is driven straight from a Go daemon over a Unix socket and everything is drawn with <a href="%3" style="text-decoration:none; color:%1;">Quickshell</a> — no webview, no Electron.', 'about page project description with embedded links, %1 is the accent color, %2 is the Dux Shell url, %3 is the Quickshell url').arg(Theme.primary).arg("https://github.com/duxlabs-dev").arg("https://quickshell.org")
                    textFormat: Text.RichText
                    font.pixelSize: Theme.fontSizeMedium
                    linkColor: Theme.primary
                    onLinkActivated: url => Qt.openUrlExternally(url)
                    color: Theme.surfaceVariantText
                    width: parent.width
                    wrapMode: Text.WordWrap

                    HoverHandler {
                        cursorShape: parent.hoveredLink ? Qt.PointingHandCursor : Qt.ArrowCursor
                    }
                }
            }
        }

        SettingsCard {
            // Docs row stays commented out until the site is live.
            // SettingsLinkRow {
            //     iconName: "menu_book"
            //     title: I18n.tr("Docs", "about page resource button")
            //     subtitle: root.host(root.docsUrl)
            //     url: root.docsUrl
            // }

            SettingsLinkRow {
                iconName: "code"
                title: I18n.tr("GitHub", "about page resource button")
                subtitle: root.host(root.githubUrl)
                url: root.githubUrl
            }
        }

        // Community placeholder: uncomment once there are links to show.
        // SettingsCard {
        //     title: I18n.tr("Community", "about page card title, chat and community links")
        //
        //     SettingsLinkRow {
        //         title: "Discord"
        //         subtitle: "—"
        //         url: ""
        //     }
        // }

        SettingsCard {
            title: I18n.tr("Backend", "about page daemon section header")

            SettingsRow {
                title: I18n.tr("Version", "about page daemon version column label")
                trailingBadge: Backend.daemonVersion || "—"
            }

            SettingsRow {
                title: I18n.tr("API", "about page daemon api version column label")
                trailingBadge: Backend.apiVersion > 0 ? `v${Backend.apiVersion}` : "—"
            }

            SettingsRow {
                title: I18n.tr("Status", "about page daemon status column label")
                trailingBadge: Backend.connected ? I18n.tr("Connected", "about page daemon status value") : I18n.tr("Offline", "about page daemon status value")

                DBadge {
                    color: Backend.connected ? Theme.success : Theme.error
                }
            }

            SettingsRow {
                visible: Backend.capabilities.length > 0
                title: I18n.tr("Capabilities", "about page daemon capabilities label")
                body: Flow {
                    width: parent.width
                    spacing: Theme.spacingS

                    Repeater {
                        model: Backend.capabilities

                        DBadge {
                            required property string modelData
                            text: modelData
                            color: Theme.withAlpha(Theme.primary, Theme.tonalTintAlpha)
                            textColor: Theme.primary
                        }
                    }
                }
            }
        }

        StyledText {
            anchors.horizontalCenter: parent.horizontalCenter
            text: I18n.tr('<a href="%2" style="text-decoration:none; color:%1;">MIT License</a>', 'about page license footer link, %2 is the license url').arg(Theme.surfaceVariantText).arg(root.licenseUrl)
            font.pixelSize: Theme.fontSizeMedium
            color: Theme.surfaceVariantText
            textFormat: Text.RichText
            wrapMode: Text.NoWrap
            onLinkActivated: url => Qt.openUrlExternally(url)

            HoverHandler {
                cursorShape: parent.hoveredLink ? Qt.PointingHandCursor : Qt.ArrowCursor
            }
        }
    }
}
