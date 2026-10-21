/*
 * Moves a torn-off file tab's window with the pointer during a GTK drag, using
 * xdg_toplevel_drag_v1.
 *
 * GTK3 keeps each drag's wl_data_source and each window's xdg_toplevel
 * private, so the shell executable defines wl_proxy_marshal_flags. Every
 * request GTK and the shell marshal passes through it unchanged to
 * wl_proxy_marshal_array_flags. Two requests are acted on, and only while the
 * shell armed them on the GTK main thread:
 *
 * - wl_data_device.start_drag: the toplevel drag is created for GTK's source
 *   first, as the protocol requires.
 * - xdg_surface.get_toplevel: the new toplevel is attached to the drag before
 *   it maps.
 *
 * Argument decoding follows the request signature of the proxy's interface.
 * libwayland lays out every proxy as a wl_object whose first member is that
 * interface; pw_toplevel_drag_init checks this on the display proxy before
 * anything is armed.
 */
#include <stdarg.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <wayland-client.h>

#define MAX_ARGS 20

extern const struct wl_interface xdg_toplevel_drag_v1_interface;

/* Object arguments need only the proxy; a new_id's interface is passed when marshalling. */
static const struct wl_interface *no_types[] = {NULL, NULL, NULL};

static const struct wl_message drag_requests[] = {
    {"destroy", "", no_types},
    {"attach", "oii", no_types},
};

const struct wl_interface xdg_toplevel_drag_v1_interface = {
    "xdg_toplevel_drag_v1", 1, 2, drag_requests, 0, NULL,
};

static const struct wl_interface *get_drag_types[] = {
    &xdg_toplevel_drag_v1_interface,
    &wl_data_source_interface,
};

static const struct wl_message manager_requests[] = {
    {"destroy", "", no_types},
    {"get_xdg_toplevel_drag", "no", get_drag_types},
};

const struct wl_interface xdg_toplevel_drag_manager_v1_interface = {
    "xdg_toplevel_drag_manager_v1", 1, 2, manager_requests, 0, NULL,
};

static struct {
    struct wl_display *display;
    struct wl_proxy *manager;
    int layout_checked;
    int arm_source;
    int arm_toplevel;
    int32_t x_offset;
    int32_t y_offset;
    struct wl_proxy *drag;
    int attached;
    int failures;
} state;

static int interface_is(const struct wl_interface *interface, const char *name)
{
    return interface && interface->name && strcmp(interface->name, name) == 0;
}

static const struct wl_interface *proxy_interface(struct wl_proxy *proxy)
{
    return *(const struct wl_interface *const *)proxy;
}

static int decode(const char *signature, union wl_argument *args, va_list ap)
{
    int count = 0;
    for (const char *type = signature; *type; type++) {
        if (*type == '?' || (*type >= '0' && *type <= '9'))
            continue;
        if (count == MAX_ARGS)
            return -1;
        switch (*type) {
        case 'i': args[count].i = va_arg(ap, int32_t); break;
        case 'u': args[count].u = va_arg(ap, uint32_t); break;
        case 'f': args[count].f = va_arg(ap, wl_fixed_t); break;
        case 's': args[count].s = va_arg(ap, const char *); break;
        case 'o': args[count].o = va_arg(ap, struct wl_object *); break;
        case 'n': args[count].o = va_arg(ap, struct wl_object *); break;
        case 'a': args[count].a = va_arg(ap, struct wl_array *); break;
        case 'h': args[count].h = va_arg(ap, int32_t); break;
        default: return -1;
        }
        count++;
    }
    return count;
}

static void create_drag(struct wl_proxy *source)
{
    union wl_argument args[2];
    args[0].n = 0;
    args[1].o = (struct wl_object *)source;
    state.drag = wl_proxy_marshal_array_flags(state.manager, 1, &xdg_toplevel_drag_v1_interface,
                                              wl_proxy_get_version(state.manager), 0, args);
    if (!state.drag)
        state.failures++;
}

static void attach(struct wl_proxy *toplevel)
{
    union wl_argument args[3];
    args[0].o = (struct wl_object *)toplevel;
    args[1].i = state.x_offset;
    args[2].i = state.y_offset;
    wl_proxy_marshal_array_flags(state.drag, 1, NULL, wl_proxy_get_version(state.drag), 0, args);
    state.attached = 1;
}

struct wl_proxy *wl_proxy_marshal_flags(struct wl_proxy *proxy, uint32_t opcode,
                                        const struct wl_interface *interface, uint32_t version,
                                        uint32_t flags, ...)
{
    union wl_argument args[MAX_ARGS];
    const struct wl_interface *own = proxy_interface(proxy);
    va_list ap;
    va_start(ap, flags);
    int count = decode(own->methods[opcode].signature, args, ap);
    va_end(ap);
    /* Every wire type is decoded; anything else means a libwayland this file
     * does not understand, and forwarding a guess would corrupt the stream. */
    if (count < 0)
        abort();

    if (state.arm_source && opcode == 0 && interface_is(own, "wl_data_device") && args[0].o) {
        state.arm_source = 0;
        create_drag((struct wl_proxy *)args[0].o);
    }
    struct wl_proxy *made = wl_proxy_marshal_array_flags(proxy, opcode, interface, version, flags, args);
    if (state.arm_toplevel && state.drag && made && opcode == 1 && interface_is(own, "xdg_surface")) {
        state.arm_toplevel = 0;
        attach(made);
    }
    return made;
}

static void global(void *data, struct wl_registry *registry, uint32_t name, const char *interface,
                   uint32_t version)
{
    (void)data;
    (void)version;
    if (strcmp(interface, "xdg_toplevel_drag_manager_v1") == 0 && !state.manager)
        state.manager = wl_registry_bind(registry, name, &xdg_toplevel_drag_manager_v1_interface, 1);
}

static void global_remove(void *data, struct wl_registry *registry, uint32_t name)
{
    (void)data;
    (void)registry;
    (void)name;
}

static const struct wl_registry_listener registry_listener = {global, global_remove};

/* Returns 1 when the compositor offers the protocol and the proxy layout holds. */
int pw_toplevel_drag_init(struct wl_display *display)
{
    if (state.display)
        return state.manager != NULL;
    state.display = display;
    if (proxy_interface((struct wl_proxy *)display) != &wl_display_interface)
        return 0;
    state.layout_checked = 1;
    struct wl_event_queue *queue = wl_display_create_queue(display);
    if (!queue)
        return 0;
    struct wl_display *wrapper = wl_proxy_create_wrapper(display);
    if (!wrapper) {
        wl_event_queue_destroy(queue);
        return 0;
    }
    wl_proxy_set_queue((struct wl_proxy *)wrapper, queue);
    struct wl_registry *registry = wl_display_get_registry(wrapper);
    wl_proxy_wrapper_destroy(wrapper);
    if (registry) {
        wl_registry_add_listener(registry, &registry_listener, NULL);
        wl_display_roundtrip_queue(display, queue);
        wl_registry_destroy(registry);
    }
    if (state.manager)
        wl_proxy_set_queue(state.manager, NULL);
    wl_event_queue_destroy(queue);
    return state.manager != NULL;
}

/* The next start_drag gets a toplevel drag. */
void pw_toplevel_drag_arm_source(void)
{
    if (state.manager && state.layout_checked && !state.drag)
        state.arm_source = 1;
}

/* The next xdg_toplevel created is attached with this offset from the pointer. */
void pw_toplevel_drag_arm_toplevel(int32_t x_offset, int32_t y_offset)
{
    if (state.drag && !state.attached) {
        state.arm_toplevel = 1;
        state.x_offset = x_offset;
        state.y_offset = y_offset;
    }
}

int pw_toplevel_drag_active(void)
{
    return state.drag != NULL;
}

int pw_toplevel_drag_attached(void)
{
    return state.attached;
}

/* After the drag ended (dropped or cancelled), release the drag object. */
void pw_toplevel_drag_finish(void)
{
    state.arm_source = 0;
    state.arm_toplevel = 0;
    if (state.drag)
        wl_proxy_marshal_array_flags(state.drag, 0, NULL, wl_proxy_get_version(state.drag),
                                     WL_MARSHAL_FLAG_DESTROY, NULL);
    state.drag = NULL;
    state.attached = 0;
}
