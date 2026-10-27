//! What a screen reader sees. The window is drawn, so there are no controls to ask: the tree is made
//! from what was drawn. Every text that went on screen is a label, and every region that answers a click
//! is a button, a tab or a switch named by the text inside it. `accesskit` turns the tree into UI
//! Automation, and only builds on demand: while no assistive technology is attached, nothing is made.

use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
};

use accesskit::{Action, ActionHandler, ActionRequest, ActivationHandler, Node, NodeId, Rect, Role, TreeId, TreeInfo, TreeUpdate};
use windows::Win32::{
    Foundation::{HWND, LPARAM, WPARAM},
    UI::WindowsAndMessaging::{PostMessageW, WM_APP},
};

use crate::{
    bay::Item,
    edit::Field,
    pages::{Act, Page},
};

/// An action asked for by an assistive technology; the window picks the node ids up.
pub const WM_ACCESS_ACTION: u32 = WM_APP + 10;
/// Assistive technology attached: draw a frame so that it gets the tree.
pub const WM_ACCESS_ACTIVE: u32 = WM_APP + 11;

type R = (f32, f32, f32, f32);

/// What a node stands for, when it is pressed.
#[derive(Clone, Debug, PartialEq)]
pub enum Target {
    Act(Act),
    Route(usize),
    Toggle(String),
    Field(Field),
}

pub struct Frame<'a> {
    pub title: &'a str,
    pub size: (f32, f32),
    pub texts: &'a [(f32, f32, f32, f32, String)],
    pub regions: &'a [(R, Act)],
    pub items: &'a [(R, String, Item)],
    pub edits: &'a [(Field, R)],
    pub focus: Option<&'a Act>,
    /// The part of the window that shows the page, in pixels; text outside it is not on screen.
    pub viewport: R,
}

const ROOT: NodeId = NodeId(1);

fn rect(r: R) -> Rect {
    Rect { x0: r.0 as f64, y0: r.1 as f64, x1: (r.0 + r.2) as f64, y1: (r.1 + r.3) as f64 }
}

fn centre_in(t: &(f32, f32, f32, f32, String), r: R) -> bool {
    let (cx, cy) = (t.0 + t.2 / 2.0, t.1 + t.3 / 2.0);
    cx >= r.0 && cx < r.0 + r.2 && cy >= r.1 && cy < r.1 + r.3
}

/// The name of a button that has no words in it (an icon).
fn fallback(act: &Act) -> &'static str {
    match act {
        Act::Import => "Import a profile",
        Act::Go(Page::Routing) => "Routing",
        Act::Go(Page::Diagnostics) => "Diagnostics",
        Act::Go(Page::Settings) => "Settings",
        Act::Go(Page::Profile(_)) => "Profile",
        _ => "Button",
    }
}

fn field_name(f: Field) -> &'static str {
    match f {
        Field::Config => "Profile text",
        Field::Username => "Username",
        Field::Password => "Password",
        Field::Rename => "Profile name",
    }
}

/// The tree for one frame, and what each of its nodes does when pressed.
pub fn build(f: &Frame) -> (TreeUpdate, HashMap<NodeId, Target>) {
    let mut targets = HashMap::new();
    // (y, x, id) of every child, to put them in reading order.
    let mut kids: Vec<(f32, f32, NodeId)> = vec![];
    let mut nodes: Vec<(NodeId, Node)> = vec![];
    let mut next = 100u64;
    let mut id = || {
        next += 1;
        NodeId(next)
    };
    let mut focus = ROOT;
    // Text in the sidebar and the title bar is always on screen; text of the page only inside its area.
    let on_screen = |t: &(f32, f32, f32, f32, String)| t.0 < f.viewport.0 || centre_in(t, f.viewport);
    // Which texts a control already speaks for.
    let mut spoken: Vec<bool> = vec![false; f.texts.len()];

    for (r, act) in f.regions {
        let words: Vec<&str> = f.texts.iter().enumerate().filter(|(_, t)| centre_in(t, *r)).map(|(i, t)| {
            spoken[i] = true;
            t.4.as_str()
        }).collect();
        let name = if words.is_empty() { fallback(act).to_string() } else { words.join(", ") };
        let role = match act {
            Act::Tab(_) => Role::Tab,
            Act::Switch(_) | Act::ProfileSwitch(..) => Role::Switch,
            _ => Role::Button,
        };
        let mut n = Node::new(role);
        n.set_label(name);
        n.set_bounds(rect(*r));
        n.add_action(Action::Click);
        let node_id = id();
        if f.focus == Some(act) {
            focus = node_id;
        }
        targets.insert(node_id, Target::Act(act.clone()));
        kids.push((r.1, r.0, node_id));
        nodes.push((node_id, n));
    }
    for (r, label, item) in f.items {
        let words: Vec<usize> = f.texts.iter().enumerate().filter(|(_, t)| centre_in(t, *r)).map(|(i, _)| i).collect();
        for i in words {
            spoken[i] = true;
        }
        let mut n = Node::new(match item {
            Item::Route(_) => Role::ListItem,
            Item::Toggle(_) => Role::Button,
        });
        n.set_label(label.clone());
        n.set_bounds(rect(*r));
        n.add_action(Action::Click);
        let node_id = id();
        targets.insert(node_id, match item {
            Item::Route(d) => Target::Route(*d),
            Item::Toggle(t) => Target::Toggle(t.clone()),
        });
        kids.push((r.1, r.0, node_id));
        nodes.push((node_id, n));
    }
    for (field, r) in f.edits {
        let mut n = Node::new(if *field == Field::Config { Role::MultilineTextInput } else { Role::TextInput });
        n.set_label(field_name(*field));
        n.set_bounds(rect(*r));
        n.add_action(Action::Focus);
        let node_id = id();
        targets.insert(node_id, Target::Field(*field));
        kids.push((r.1, r.0, node_id));
        nodes.push((node_id, n));
    }
    for (i, t) in f.texts.iter().enumerate() {
        if spoken[i] || !on_screen(t) {
            continue;
        }
        let mut n = Node::new(Role::Label);
        n.set_value(t.4.clone());
        n.set_bounds(rect((t.0, t.1, t.2, t.3)));
        let node_id = id();
        kids.push((t.1, t.0, node_id));
        nodes.push((node_id, n));
    }
    kids.sort_by(|a, b| a.0.total_cmp(&b.0).then(a.1.total_cmp(&b.1)));
    let mut root = Node::new(Role::Window);
    root.set_label(f.title);
    root.set_bounds(rect((0.0, 0.0, f.size.0, f.size.1)));
    root.set_children(kids.into_iter().map(|k| k.2).collect::<Vec<_>>());
    nodes.push((ROOT, root));
    (TreeUpdate { nodes, tree: Some(TreeInfo::new(ROOT)), tree_id: TreeId::ROOT, focus }, targets)
}

/// The first tree, before there is a frame: just the window. A frame follows at once.
pub struct Activate(pub isize);

impl ActivationHandler for Activate {
    fn request_initial_tree(&mut self) -> Option<TreeUpdate> {
        let mut root = Node::new(Role::Window);
        root.set_label("Plaitway");
        unsafe {
            let _ = PostMessageW(Some(HWND(self.0 as *mut _)), WM_ACCESS_ACTIVE, WPARAM(0), LPARAM(0));
        }
        Some(TreeUpdate { nodes: vec![(ROOT, root)], tree: Some(TreeInfo::new(ROOT)), tree_id: TreeId::ROOT, focus: ROOT })
    }
}

/// Actions arrive on another thread; they are queued and the window is told.
pub struct Actions {
    pub hwnd: isize,
    pub queue: Arc<Mutex<Vec<NodeId>>>,
}

impl ActionHandler for Actions {
    fn do_action(&mut self, request: ActionRequest) {
        if matches!(request.action, Action::Click | Action::Focus) {
            self.queue.lock().unwrap().push(request.target_node);
            unsafe {
                let _ = PostMessageW(Some(HWND(self.hwnd as *mut _)), WM_ACCESS_ACTION, WPARAM(0), LPARAM(0));
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn frame<'a>(texts: &'a [(f32, f32, f32, f32, String)], regions: &'a [(R, Act)], items: &'a [(R, String, Item)], edits: &'a [(Field, R)], focus: Option<&'a Act>) -> Frame<'a> {
        Frame { title: "Plaitway", size: (1600.0, 1000.0), texts, regions, items, edits, focus, viewport: (310.0, 40.0, 1290.0, 960.0) }
    }

    fn t(x: f32, y: f32, s: &str) -> (f32, f32, f32, f32, String) {
        (x, y, 80.0, 20.0, s.to_string())
    }

    fn by_label<'a>(u: &'a TreeUpdate, label: &str) -> Option<&'a (NodeId, Node)> {
        u.nodes.iter().find(|(_, n)| n.label() == Some(label))
    }

    #[test]
    fn a_button_is_named_by_the_words_in_it_and_they_are_not_said_twice() {
        let texts = [t(20.0, 100.0, "Home"), t(20.0, 125.0, "WireGuard"), t(400.0, 300.0, "Connected")];
        let regions = [((8.0, 90.0, 280.0, 60.0), Act::Go(Page::Profile("home".into())))];
        let (u, targets) = build(&frame(&texts, &regions, &[], &[], None));
        let (id, n) = by_label(&u, "Home, WireGuard").expect("the nav row");
        assert_eq!(n.role(), Role::Button);
        assert_eq!(targets[id], Target::Act(Act::Go(Page::Profile("home".into()))));
        assert!(by_label(&u, "Home").is_none(), "the words inside a button are its name, not separate labels");
        assert!(u.nodes.iter().any(|(_, n)| n.role() == Role::Label && n.value() == Some("Connected")), "text elsewhere is read as it is");
    }

    #[test]
    fn an_icon_button_has_a_name_from_what_it_does() {
        let regions = [((250.0, 120.0, 24.0, 24.0), Act::Import)];
        let (u, _) = build(&frame(&[], &regions, &[], &[], None));
        assert!(by_label(&u, "Import a profile").is_some());
    }

    #[test]
    fn tabs_and_switches_have_their_own_roles() {
        let texts = [t(380.0, 180.0, "Routes"), t(400.0, 400.0, "Start with Windows")];
        let regions = [((370.0, 170.0, 80.0, 36.0), Act::Tab(1)), ((320.0, 380.0, 1000.0, 46.0), Act::Switch(0))];
        let (u, _) = build(&frame(&texts, &regions, &[], &[], None));
        assert_eq!(by_label(&u, "Routes").unwrap().1.role(), Role::Tab);
        assert_eq!(by_label(&u, "Start with Windows").unwrap().1.role(), Role::Switch);
    }

    #[test]
    fn the_keyboard_focus_is_the_node_of_the_thing_it_is_on() {
        let regions = [((8.0, 90.0, 280.0, 60.0), Act::Go(Page::Settings)), ((8.0, 160.0, 280.0, 60.0), Act::Go(Page::Diagnostics))];
        let focus = Act::Go(Page::Diagnostics);
        let (u, targets) = build(&frame(&[], &regions, &[], &[], Some(&focus)));
        assert_eq!(targets[&u.focus], Target::Act(Act::Go(Page::Diagnostics)));
        let (u, _) = build(&frame(&[], &regions, &[], &[], None));
        assert_eq!(u.focus, ROOT, "without a focus the window has it");
    }

    #[test]
    fn text_boxes_and_routing_items_are_in_the_tree_with_what_they_do() {
        let edits = [(Field::Password, (400.0, 300.0, 300.0, 30.0)), (Field::Config, (400.0, 400.0, 900.0, 400.0))];
        let items = [((330.0, 200.0, 300.0, 70.0), "Default route: Home holds it".to_string(), Item::Route(0)), ((1300.0, 250.0, 120.0, 30.0), "Disconnect Home".to_string(), Item::Toggle("home".into()))];
        let (u, targets) = build(&frame(&[], &[], &items, &edits, None));
        assert_eq!(by_label(&u, "Password").unwrap().1.role(), Role::TextInput);
        assert_eq!(by_label(&u, "Profile text").unwrap().1.role(), Role::MultilineTextInput);
        let (id, n) = by_label(&u, "Disconnect Home").unwrap();
        assert_eq!(n.role(), Role::Button);
        assert_eq!(targets[id], Target::Toggle("home".into()));
        assert_eq!(targets[&by_label(&u, "Default route: Home holds it").unwrap().0], Target::Route(0));
    }

    #[test]
    fn the_window_lists_its_children_in_reading_order() {
        let texts = [t(500.0, 300.0, "second"), t(400.0, 100.0, "first"), t(900.0, 300.0, "third")];
        let (u, _) = build(&frame(&texts, &[], &[], &[], None));
        let root = &u.nodes.iter().find(|(id, _)| *id == ROOT).unwrap().1;
        let names: Vec<String> = root.children().iter().map(|c| u.nodes.iter().find(|(id, _)| id == c).unwrap().1.value().unwrap().to_string()).collect();
        assert_eq!(names, ["first", "second", "third"]);
        assert_eq!(root.role(), Role::Window);
        assert_eq!(root.label(), Some("Plaitway"));
    }

    #[test]
    fn every_child_is_in_the_tree_exactly_once() {
        let texts: Vec<_> = (0..20).map(|i| t(400.0, 100.0 + i as f32 * 30.0, &format!("line {i}"))).collect();
        let regions = [((8.0, 90.0, 280.0, 60.0), Act::Go(Page::Settings))];
        let (u, _) = build(&frame(&texts, &regions, &[], &[], None));
        let root = &u.nodes.iter().find(|(id, _)| *id == ROOT).unwrap().1;
        let mut seen = std::collections::HashSet::new();
        for c in root.children() {
            assert!(seen.insert(*c), "{c:?} twice");
            assert!(u.nodes.iter().any(|(id, _)| id == c), "{c:?} has no node");
        }
        assert_eq!(seen.len() + 1, u.nodes.len());
    }
}

